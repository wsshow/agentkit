package agentkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	officialmcp "github.com/cloudwego/eino-ext/components/tool/mcp/officialmcp"
	mcpsession "github.com/cloudwego/eino-ext/components/tool/mcp/officialmcp/session"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// DefaultMCPMaxResultChars 是单次 MCP 工具结果默认保留的最大字符数。
	DefaultMCPMaxResultChars = 100_000
	// DefaultMCPMaxDescriptionChars 是单个 MCP 工具描述默认保留的最大字符数。
	DefaultMCPMaxDescriptionChars = 4_000
	// DefaultMCPInitializationTimeout 是每个 MCP 服务器连接与工具发现的默认总时限。
	DefaultMCPInitializationTimeout = 30 * time.Second
)

// MCPTransport 表示 MCP 服务器传输方式。
type MCPTransport string

const (
	MCPTransportStdio          MCPTransport = mcpsession.TransportStdio
	MCPTransportSSE            MCPTransport = mcpsession.TransportSSE
	MCPTransportStreamableHTTP MCPTransport = mcpsession.TransportStreamableHTTP
)

// MCPClientSession 是已连接的 MCP 会话。
// AgentKit 会取得传入会话的所有权，并在 Agent.Close 时关闭它。
type MCPClientSession interface {
	officialmcp.ClientSession
	Close() error
}

// MCPConfig 配置 Agent 使用的 MCP 服务器。
type MCPConfig struct {
	Servers []MCPServerConfig

	ClientName    string        // MCP 客户端名称，默认 "agentkit"
	ClientVersion string        // MCP 客户端版本，默认 "dev"
	KeepAlive     time.Duration // 大于 0 时定期 ping 服务器
	// InitializationTimeout 限制每个服务器的连接与初始工具发现；零值使用 30 秒默认值。
	InitializationTimeout time.Duration

	// 0 使用安全默认值，-1 关闭限制，正数使用指定限制。
	MaxResultChars      int
	MaxDescriptionChars int
}

// MCPServerConfig 配置一个 MCP 服务器。
// Session 与 Transport 二选一；Session 适合已连接的自定义或进程内会话。
type MCPServerConfig struct {
	Name       string
	Transport  MCPTransport
	URL        string
	Command    string
	Args       []string
	Env        map[string]string
	WorkingDir string
	Headers    map[string]string
	HTTPClient *http.Client

	Session    MCPClientSession
	ToolNames  []string // 为空时加载服务器的全部工具
	ToolPrefix string   // 暴露给模型的工具名前缀，用于避免多服务器重名
}

type managedMCPConnection struct {
	name    string
	session MCPClientSession
}

func validateMCPConfig(cfg *MCPConfig) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.Servers) == 0 {
		return errors.New("agentkit: MCP requires at least one server")
	}
	if cfg.KeepAlive < 0 {
		return errors.New("agentkit: MCP keep alive must not be negative")
	}
	if cfg.InitializationTimeout < 0 {
		return errors.New("agentkit: MCP initialization timeout must not be negative")
	}
	if cfg.MaxResultChars < -1 {
		return errors.New("agentkit: MCP max result chars must be -1, 0, or positive")
	}
	if cfg.MaxDescriptionChars < -1 {
		return errors.New("agentkit: MCP max description chars must be -1, 0, or positive")
	}
	if cfg.ClientName != "" && strings.TrimSpace(cfg.ClientName) == "" {
		return errors.New("agentkit: MCP client name must not be blank")
	}
	if cfg.ClientName != strings.TrimSpace(cfg.ClientName) {
		return fmt.Errorf("agentkit: MCP client name must not have surrounding whitespace: %q", cfg.ClientName)
	}
	if cfg.ClientVersion != "" && strings.TrimSpace(cfg.ClientVersion) == "" {
		return errors.New("agentkit: MCP client version must not be blank")
	}
	if cfg.ClientVersion != strings.TrimSpace(cfg.ClientVersion) {
		return fmt.Errorf("agentkit: MCP client version must not have surrounding whitespace: %q", cfg.ClientVersion)
	}

	seenServers := make(map[string]struct{}, len(cfg.Servers))
	for i := range cfg.Servers {
		server := &cfg.Servers[i]
		if strings.TrimSpace(server.Name) == "" {
			return fmt.Errorf("agentkit: MCP server %d name is required", i)
		}
		if server.Name != strings.TrimSpace(server.Name) {
			return fmt.Errorf("agentkit: MCP server name must not have surrounding whitespace: %q", server.Name)
		}
		if _, ok := seenServers[server.Name]; ok {
			return fmt.Errorf("agentkit: duplicate MCP server name %q", server.Name)
		}
		seenServers[server.Name] = struct{}{}
		if server.ToolPrefix != "" && strings.TrimSpace(server.ToolPrefix) == "" {
			return fmt.Errorf("agentkit: MCP server %q tool prefix must not be blank", server.Name)
		}
		if server.ToolPrefix != strings.TrimSpace(server.ToolPrefix) {
			return fmt.Errorf("agentkit: MCP server %q tool prefix must not have surrounding whitespace", server.Name)
		}
		seenTools := make(map[string]struct{}, len(server.ToolNames))
		for _, name := range server.ToolNames {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("agentkit: MCP server %q tool name must not be blank", server.Name)
			}
			if name != strings.TrimSpace(name) {
				return fmt.Errorf("agentkit: MCP server %q tool name must not have surrounding whitespace: %q", server.Name, name)
			}
			if _, ok := seenTools[name]; ok {
				return fmt.Errorf("agentkit: MCP server %q has duplicate tool filter %q", server.Name, name)
			}
			seenTools[name] = struct{}{}
		}
		if err := validateMCPServerSource(server); err != nil {
			return err
		}
	}
	return nil
}

func validateMCPServerSource(server *MCPServerConfig) error {
	if server.Session != nil {
		if server.Transport != "" || server.URL != "" || server.Command != "" || len(server.Args) > 0 || len(server.Env) > 0 || server.WorkingDir != "" || len(server.Headers) > 0 || server.HTTPClient != nil {
			return fmt.Errorf("agentkit: MCP server %q session and transport settings cannot be configured together", server.Name)
		}
		return nil
	}

	switch server.Transport {
	case MCPTransportStdio:
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("agentkit: MCP server %q stdio command is required", server.Name)
		}
		if server.Command != strings.TrimSpace(server.Command) {
			return fmt.Errorf("agentkit: MCP server %q command must not have surrounding whitespace", server.Name)
		}
		if server.URL != "" || len(server.Headers) > 0 || server.HTTPClient != nil {
			return fmt.Errorf("agentkit: MCP server %q stdio transport does not accept URL or HTTP settings", server.Name)
		}
	case MCPTransportSSE, MCPTransportStreamableHTTP:
		if err := validateMCPURL(server.URL); err != nil {
			return fmt.Errorf("agentkit: MCP server %q: %w", server.Name, err)
		}
		if server.Command != "" || len(server.Args) > 0 || len(server.Env) > 0 || server.WorkingDir != "" {
			return fmt.Errorf("agentkit: MCP server %q HTTP transport does not accept command settings", server.Name)
		}
	case "":
		return fmt.Errorf("agentkit: MCP server %q transport is required", server.Name)
	default:
		return fmt.Errorf("agentkit: MCP server %q has unsupported transport %q", server.Name, server.Transport)
	}
	return nil
}

func validateMCPURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid transport URL: %w", err)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return fmt.Errorf("transport URL must be absolute: %s", rawURL)
	}
	return nil
}

func connectMCP(ctx context.Context, cfg *MCPConfig) ([]Tool, []managedMCPConnection, error) {
	clientName := cfg.ClientName
	if clientName == "" {
		clientName = "agentkit"
	}
	clientVersion := cfg.ClientVersion
	if clientVersion == "" {
		clientVersion = "dev"
	}
	maxResultChars := configuredMCPLimit(cfg.MaxResultChars, DefaultMCPMaxResultChars)
	maxDescriptionChars := configuredMCPLimit(cfg.MaxDescriptionChars, DefaultMCPMaxDescriptionChars)
	initializationTimeout := mcpInitializationTimeout(cfg)

	var tools []Tool
	connections := make([]managedMCPConnection, 0, len(cfg.Servers))
	fail := func(err error) ([]Tool, []managedMCPConnection, error) {
		return nil, nil, errors.Join(err, closeMCPConnectionsAfterInitialization(ctx, cfg, connections))
	}

	for _, server := range cfg.Servers {
		serverCtx, cancelServer := context.WithTimeout(ctx, initializationTimeout)
		connection := server.Session
		if connection == nil {
			managed, err := mcpsession.Connect(serverCtx, mcpsession.ServerConfig{
				Name: server.Name,
				Transport: mcpsession.TransportConfig{
					Type:       string(server.Transport),
					URL:        server.URL,
					Command:    server.Command,
					Args:       append([]string(nil), server.Args...),
					Env:        cloneStringMap(server.Env),
					Headers:    cloneStringMap(server.Headers),
					CWD:        server.WorkingDir,
					HTTPClient: server.HTTPClient,
				},
				Client:        &protocol.Implementation{Name: clientName, Version: clientVersion},
				ClientOptions: &protocol.ClientOptions{KeepAlive: cfg.KeepAlive},
			})
			if err != nil {
				cancelServer()
				return fail(fmt.Errorf("agentkit: connect MCP server %q: %w", server.Name, err))
			}
			connection = managed
		}
		connections = append(connections, managedMCPConnection{name: server.Name, session: connection})

		toolConfig := &officialmcp.Config{
			Cli:           connection,
			ServerName:    server.Name,
			ToolNameList:  append([]string(nil), server.ToolNames...),
			ListToolsMode: officialmcp.ListToolsAllPages,
			ResultPolicy: &officialmcp.ResultPolicy{
				MaxChars:          maxResultChars,
				PreserveTailChars: preserveTailChars(maxResultChars),
			},
			DescriptionPolicy: &officialmcp.DescriptionPolicy{MaxChars: maxDescriptionChars},
		}
		if server.ToolPrefix != "" {
			prefix := server.ToolPrefix
			toolConfig.ToolNameMapper = func(_ context.Context, input officialmcp.ToolNameMapperInput) (officialmcp.ToolNameMapperOutput, error) {
				return officialmcp.ToolNameMapperOutput{ExposedName: prefix + input.Tool.Name}, nil
			}
		}
		serverTools, err := officialmcp.GetTools(serverCtx, toolConfig)
		if err != nil {
			cancelServer()
			return fail(fmt.Errorf("agentkit: load tools from MCP server %q: %w", server.Name, err))
		}
		if len(serverTools) == 0 {
			cancelServer()
			return fail(fmt.Errorf("agentkit: MCP server %q returned no matching tools", server.Name))
		}
		if err := validateMCPTools(serverCtx, server, serverTools); err != nil {
			cancelServer()
			return fail(err)
		}
		cancelServer()
		tools = append(tools, serverTools...)
	}
	return tools, connections, nil
}

func validateMCPTools(ctx context.Context, server MCPServerConfig, tools []Tool) error {
	matched := make(map[string]struct{}, len(tools))
	for _, item := range tools {
		info, err := inspectToolInfo(ctx, item)
		if err != nil {
			return fmt.Errorf("agentkit: inspect tool from MCP server %q: %w", server.Name, err)
		}
		if info == nil {
			return fmt.Errorf("agentkit: MCP server %q returned a tool without metadata", server.Name)
		}
		if err := validateMCPToolName(info.Name); err != nil {
			return fmt.Errorf("agentkit: MCP server %q exposed invalid tool name %q: %w", server.Name, info.Name, err)
		}
		if rawName, ok := info.Extra[officialmcp.ExtraMCPRawToolName].(string); ok {
			matched[rawName] = struct{}{}
		}
	}
	for _, name := range server.ToolNames {
		if _, ok := matched[name]; !ok {
			return fmt.Errorf("agentkit: MCP server %q does not provide requested tool %q", server.Name, name)
		}
	}
	return nil
}

func validateMCPToolName(name string) error {
	if name == "" {
		return errors.New("name is empty")
	}
	if len(name) > 128 {
		return fmt.Errorf("name exceeds 128 bytes: %d", len(name))
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return fmt.Errorf("name contains unsupported character %q", char)
	}
	return nil
}

func configuredMCPLimit(value, defaultValue int) int {
	if value == 0 {
		return defaultValue
	}
	if value < 0 {
		return 0
	}
	return value
}

func preserveTailChars(maxChars int) int {
	if maxChars <= 0 {
		return 0
	}
	tail := maxChars / 10
	if tail > 2_000 {
		return 2_000
	}
	return tail
}

func mcpInitializationTimeout(cfg *MCPConfig) time.Duration {
	if cfg != nil && cfg.InitializationTimeout > 0 {
		return cfg.InitializationTimeout
	}
	return DefaultMCPInitializationTimeout
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func validateCombinedToolNames(ctx context.Context, tools []Tool, skills *SkillsConfig) error {
	seen := make(map[string]struct{}, len(tools)+1)
	if skills != nil {
		name := skills.ToolName
		if name == "" {
			name = "skill"
		}
		seen[name] = struct{}{}
	}
	for index, item := range tools {
		if item == nil {
			return fmt.Errorf("agentkit: tool %d is nil", index)
		}
		info, err := inspectToolInfo(ctx, item)
		if err != nil {
			return fmt.Errorf("agentkit: inspect tool %d: %w", index, err)
		}
		if info == nil || strings.TrimSpace(info.Name) == "" {
			return fmt.Errorf("agentkit: tool %d has no name", index)
		}
		if _, ok := seen[info.Name]; ok {
			return fmt.Errorf("agentkit: duplicate tool name %q; tool names must be unique (use MCP ToolPrefix when needed)", info.Name)
		}
		seen[info.Name] = struct{}{}
	}
	return nil
}

func closeMCPConnections(connections []managedMCPConnection) error {
	var errs []error
	for index := len(connections) - 1; index >= 0; index-- {
		connection := connections[index]
		if connection.session == nil {
			continue
		}
		if err := connection.session.Close(); err != nil {
			errs = append(errs, fmt.Errorf("agentkit: close MCP server %q: %w", connection.name, err))
		}
	}
	return errors.Join(errs...)
}

func closeMCPConnectionsAfterInitialization(ctx context.Context, cfg *MCPConfig, connections []managedMCPConnection) error {
	if len(connections) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.TODO()
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mcpInitializationTimeout(cfg))
	defer cancel()
	closed := make(chan error, 1)
	go func() {
		closed <- closeMCPConnections(connections)
	}()
	select {
	case err := <-closed:
		return err
	case <-cleanupCtx.Done():
		return fmt.Errorf("agentkit: close MCP connections after initialization: %w", cleanupCtx.Err())
	}
}
