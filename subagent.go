package agentkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/patchtoolcalls"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

const (
	// DefaultSubAgentMaxDelegations 限制一次顶层运行最多启动的子 Agent 调用数。
	DefaultSubAgentMaxDelegations = 8
	// DefaultSubAgentMaxParallel 限制不同子 Agent 的并行调用数。
	DefaultSubAgentMaxParallel = 4
	// DefaultSubAgentTimeout 限制一次子 Agent 调用的最长时间。
	DefaultSubAgentTimeout = 10 * time.Minute
)

var (
	// ErrSubAgentBusy 表示同一个子 Agent 已有调用正在执行或等待结果入账。
	ErrSubAgentBusy = errors.New("agentkit: sub-agent is busy")
	// ErrSubAgentBudgetExceeded 表示本次顶层运行已达到子 Agent 委派次数上限。
	ErrSubAgentBudgetExceeded = errors.New("agentkit: sub-agent delegation budget exceeded")
)

// SubAgentConfig 声明一个由主 Agent 按需调用的专业子 Agent。
// 子 Agent 使用独立对话上下文，默认只接收委派请求并向父 Agent 返回最终文本。
type SubAgentConfig struct {
	Name         string
	Description  string
	SystemPrompt string

	// Model 为空时继承父 Agent 的模型。
	Model ChatModel
	// Tools 只属于当前子 Agent；父 Agent 的工具不会自动继承。
	Tools      []Tool
	ToolPolicy *ToolPolicy
	Handlers   []ChatModelAgentMiddleware

	// ModelRetryConfig 与 ModelFailoverConfig 为空时继承父 Agent 的策略。
	ModelRetryConfig    *ModelRetryConfig
	ModelFailoverConfig *ModelFailoverConfig
	MaxIterations       int

	Skills     *SkillsConfig
	MCP        *MCPConfig
	ToolSearch *ToolSearchConfig

	// IncludeHistory 显式允许把父 Agent 的聊天历史改写后交给子 Agent。
	// 默认 false，只传递本次委派请求以保持上下文隔离。
	IncludeHistory bool
}

// SubAgentPolicy 为全部子 Agent 配置有界执行默认值。
// 零值使用 DefaultSubAgentMaxDelegations、DefaultSubAgentMaxParallel 和 DefaultSubAgentTimeout。
type SubAgentPolicy struct {
	MaxDelegations int
	MaxParallel    int
	Timeout        time.Duration
}

// DelegationInfo 标识一次父 Agent 到子 Agent 的委派。
type DelegationInfo struct {
	ID          string
	ParentAgent string
	Agent       string
	Path        []string
}

type subAgentRuntime struct {
	parent         *Agent
	parentName     string
	maxDelegations int
	timeout        time.Duration
	parallel       chan struct{}
	agents         map[string]struct{}
	toolToAgent    map[string]string

	mu            sync.Mutex
	delegations   int
	byID          map[string]*subAgentDelegation
	activeByAgent map[string]*subAgentDelegation
	pending       map[string][]string
	nestedTools   map[string]toolCallInfo
	nestedAgents  map[string]string
	usage         *TokenUsage
}

type subAgentDelegation struct {
	info     DelegationInfo
	counted  bool
	inFlight bool
	err      error
}

type subAgentTool struct {
	name    string
	base    einotool.InvokableTool
	runtime *subAgentRuntime
}

func newSubAgentRuntime(parent *Agent, parentName string, configs []SubAgentConfig, policy *SubAgentPolicy) *subAgentRuntime {
	maxDelegations := DefaultSubAgentMaxDelegations
	maxParallel := DefaultSubAgentMaxParallel
	timeout := DefaultSubAgentTimeout
	if policy != nil {
		if policy.MaxDelegations > 0 {
			maxDelegations = policy.MaxDelegations
		}
		if policy.MaxParallel > 0 {
			maxParallel = policy.MaxParallel
		}
		if policy.Timeout > 0 {
			timeout = policy.Timeout
		}
	}
	if len(configs) > 0 && maxParallel > len(configs) {
		maxParallel = len(configs)
	}
	runtime := &subAgentRuntime{
		parent:         parent,
		parentName:     parentName,
		maxDelegations: maxDelegations,
		timeout:        timeout,
		parallel:       make(chan struct{}, maxParallel),
		agents:         make(map[string]struct{}, len(configs)),
		toolToAgent:    make(map[string]string, len(configs)),
		byID:           make(map[string]*subAgentDelegation),
		activeByAgent:  make(map[string]*subAgentDelegation),
		pending:        make(map[string][]string),
		nestedTools:    make(map[string]toolCallInfo),
		nestedAgents:   make(map[string]string),
	}
	for _, config := range configs {
		runtime.agents[config.Name] = struct{}{}
		runtime.toolToAgent[config.Name] = config.Name
	}
	return runtime
}

func validateSubAgentConfig(parentName string, configs []SubAgentConfig, policy *SubAgentPolicy) error {
	if policy != nil {
		if policy.MaxDelegations < 0 {
			return errors.New("agentkit: sub-agent max delegations must not be negative")
		}
		if policy.MaxParallel < 0 {
			return errors.New("agentkit: sub-agent max parallel must not be negative")
		}
		if policy.Timeout < 0 {
			return errors.New("agentkit: sub-agent timeout must not be negative")
		}
	}
	names := make(map[string]struct{}, len(configs))
	for index, config := range configs {
		prefix := fmt.Sprintf("agentkit: sub-agent %d", index)
		if config.Name == "" || strings.TrimSpace(config.Name) != config.Name {
			return fmt.Errorf("%s name must be non-empty without surrounding whitespace", prefix)
		}
		if config.Name == parentName {
			return fmt.Errorf("%s name %q conflicts with the parent agent", prefix, config.Name)
		}
		if _, exists := names[config.Name]; exists {
			return fmt.Errorf("agentkit: duplicate sub-agent name %q", config.Name)
		}
		names[config.Name] = struct{}{}
		if strings.TrimSpace(config.Description) == "" {
			return fmt.Errorf("%s description is required", prefix)
		}
		if config.MaxIterations < 0 {
			return fmt.Errorf("%s max iterations must not be negative", prefix)
		}
		for handlerIndex, handler := range config.Handlers {
			if handler == nil {
				return fmt.Errorf("%s middleware %d is nil", prefix, handlerIndex)
			}
		}
		if err := validateSkillsConfig(config.Skills); err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
		if err := validateMCPConfig(config.MCP); err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
		if err := validateToolSearchConfig(config.ToolSearch); err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
	}
	return nil
}

func buildSubAgentTools(ctx context.Context, parent *Agent, configs []SubAgentConfig) ([]Tool, []managedMCPConnection, error) {
	tools := make([]Tool, 0, len(configs))
	var connections []managedMCPConnection
	for index := range configs {
		item, childConnections, err := buildSubAgentTool(ctx, parent, configs[index])
		if err != nil {
			return nil, nil, errors.Join(
				fmt.Errorf("agentkit: configure sub-agent %q: %w", configs[index].Name, err),
				closeMCPConnectionsAfterInitialization(ctx, nil, connections),
			)
		}
		connections = append(connections, childConnections...)
		tools = append(tools, item)
	}
	return tools, connections, nil
}

func buildSubAgentTool(ctx context.Context, parent *Agent, config SubAgentConfig) (Tool, []managedMCPConnection, error) {
	model := parent.model
	if config.Model != nil {
		model = guardChatModel(config.Model)
	}
	retry := parent.modelRetry
	if config.ModelRetryConfig != nil {
		retry = guardedModelRetryConfig(parent, config.ModelRetryConfig)
	}
	failover := parent.modelFailover
	if config.ModelFailoverConfig != nil {
		failover = guardedModelFailoverConfig(parent, config.ModelFailoverConfig)
	}
	maxIterations := config.MaxIterations
	if maxIterations == 0 {
		maxIterations = 20
	}

	handlers := make([]ChatModelAgentMiddleware, 0, len(config.Handlers)+3)
	for _, handler := range config.Handlers {
		handlers = append(handlers, guardAgentMiddleware(handler))
	}
	if config.ToolSearch != nil {
		middleware, err := newToolSearchMiddleware(ctx, config.ToolSearch)
		if err != nil {
			return nil, nil, err
		}
		handlers = append(handlers, middleware)
	}
	toolCallRepair, err := adkPatchToolCalls(ctx)
	if err != nil {
		return nil, nil, err
	}
	handlers = append(handlers, toolCallRepair)
	if config.Skills != nil {
		middleware, err := newSkillsMiddleware(ctx, config.Skills)
		if err != nil {
			return nil, nil, err
		}
		handlers = append(handlers, middleware)
	}

	tools := append([]Tool(nil), config.Tools...)
	var connections []managedMCPConnection
	if config.MCP != nil {
		mcpTools, opened, err := connectMCP(ctx, config.MCP)
		if err != nil {
			return nil, nil, err
		}
		connections = opened
		tools = append(tools, mcpTools...)
	}
	cleanupOnError := func(err error) (Tool, []managedMCPConnection, error) {
		return nil, nil, errors.Join(err, closeMCPConnectionsAfterInitialization(ctx, config.MCP, connections))
	}

	allTools := append(append([]Tool(nil), tools...), dynamicTools(config.ToolSearch)...)
	if err := validateCombinedToolNames(ctx, allTools, config.Skills); err != nil {
		return cleanupOnError(err)
	}
	if err := validateReservedToolNames(ctx, allTools, config.ToolSearch, config.ToolPolicy); err != nil {
		return cleanupOnError(err)
	}
	if _, err := validateToolPolicy(ctx, allTools, config.Skills, config.ToolPolicy); err != nil {
		return cleanupOnError(err)
	}

	childConfig := &adk.ChatModelAgentConfig{
		Name:                config.Name,
		Description:         config.Description,
		Instruction:         config.SystemPrompt,
		Model:               model,
		MaxIterations:       maxIterations,
		Handlers:            handlers,
		ModelRetryConfig:    retry,
		ModelFailoverConfig: failover,
	}
	if len(tools) > 0 || config.ToolPolicy != nil || config.Skills != nil || config.ToolSearch != nil {
		toolsConfig := config.ToolPolicy.toolsNodeConfig(tools)
		toolsConfig.ToolCallMiddlewares = append(
			[]compose.ToolMiddleware{subAgentToolContextMiddleware(parent, config.Name)},
			toolsConfig.ToolCallMiddlewares...,
		)
		childConfig.ToolsConfig = adk.ToolsConfig{ToolsNodeConfig: toolsConfig}
	}
	child, err := adk.NewChatModelAgent(ctx, childConfig)
	if err != nil {
		return cleanupOnError(err)
	}
	var options []adk.AgentToolOption
	if config.IncludeHistory {
		options = append(options, adk.WithFullChatHistoryAsInput())
	}
	base, ok := adk.NewAgentTool(ctx, child, options...).(einotool.InvokableTool)
	if !ok {
		return cleanupOnError(errors.New("agent tool is not invokable"))
	}
	return &subAgentTool{name: config.Name, base: base, runtime: parent.subAgents}, connections, nil
}

func adkPatchToolCalls(ctx context.Context) (ChatModelAgentMiddleware, error) {
	return patchtoolcalls.New(ctx, nil)
}

func (t *subAgentTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.base.Info(ctx)
}

func (t *subAgentTool) InvokableRun(ctx context.Context, arguments string, options ...einotool.Option) (result string, err error) {
	callID := compose.GetToolCallID(ctx)
	runCtx, cancel, info, err := t.runtime.begin(ctx, t.name, callID)
	if err != nil {
		t.runtime.fail(callID, t.name, err)
		t.runtime.emitTerminalEnd(callID)
		return "", err
	}
	defer cancel()
	defer func() {
		if t.runtime.endInvocation(info.ID, err) {
			t.runtime.emitTerminalEnd(info.ID)
		}
	}()
	return t.base.InvokableRun(runCtx, arguments, options...)
}

func (r *subAgentRuntime) begin(ctx context.Context, agentName, callID string) (context.Context, context.CancelFunc, DelegationInfo, error) {
	if ctx == nil {
		return nil, func() {}, DelegationInfo{}, errors.New("agentkit: context is required")
	}
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	r.mu.Lock()
	if callID == "" {
		if ids := r.pending[agentName]; len(ids) > 0 {
			callID = ids[0]
			r.pending[agentName] = ids[1:]
		} else {
			callID = "delegation/" + uuid.NewString()
		}
	}
	delegation := r.byID[callID]
	created := false
	if delegation == nil {
		delegation = r.newDelegation(callID, agentName)
		r.byID[callID] = delegation
		created = true
	}
	if current := r.activeByAgent[agentName]; current != nil && current != delegation {
		r.mu.Unlock()
		cancel()
		return nil, func() {}, cloneDelegationInfo(delegation.info), fmt.Errorf("%w: %s", ErrSubAgentBusy, agentName)
	}
	if delegation.inFlight {
		r.mu.Unlock()
		cancel()
		return nil, func() {}, cloneDelegationInfo(delegation.info), fmt.Errorf("%w: %s", ErrSubAgentBusy, agentName)
	}
	if !delegation.counted {
		if r.delegations >= r.maxDelegations {
			budgetErr := fmt.Errorf("%w: maximum %d", ErrSubAgentBudgetExceeded, r.maxDelegations)
			delegation.err = budgetErr
			r.mu.Unlock()
			cancel()
			return nil, func() {}, cloneDelegationInfo(delegation.info), budgetErr
		}
		r.delegations++
		delegation.counted = true
	}
	delegation.inFlight = true
	r.activeByAgent[agentName] = delegation
	info := cloneDelegationInfo(delegation.info)
	r.mu.Unlock()

	select {
	case r.parallel <- struct{}{}:
	case <-runCtx.Done():
		r.mu.Lock()
		delegation.inFlight = false
		delegation.err = runCtx.Err()
		if r.activeByAgent[agentName] == delegation {
			delete(r.activeByAgent, agentName)
		}
		r.mu.Unlock()
		cancel()
		return nil, func() {}, info, runCtx.Err()
	}
	release := func() {
		<-r.parallel
		cancel()
	}

	if created {
		r.parent.emtr.Emit(Event{Type: EventDelegationStart, Agent: agentName, Delegation: &info})
	}
	return runCtx, release, info, nil
}

func (r *subAgentRuntime) newDelegation(callID, agentName string) *subAgentDelegation {
	return &subAgentDelegation{info: DelegationInfo{
		ID:          callID,
		ParentAgent: r.parentName,
		Agent:       agentName,
		Path:        []string{r.parentName, agentName},
	}}
}

func (r *subAgentRuntime) prepare(calls []schema.ToolCall) []DelegationInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	infos := make([]DelegationInfo, 0, len(calls))
	for index := range calls {
		call := &calls[index]
		agentName := r.toolToAgent[call.Function.Name]
		if agentName == "" {
			continue
		}
		callID := call.ID
		if callID == "" {
			callID = "delegation/" + uuid.NewString()
			call.ID = callID
			r.pending[agentName] = append(r.pending[agentName], callID)
		}
		delegation := r.byID[callID]
		if delegation == nil {
			delegation = r.newDelegation(callID, agentName)
			r.byID[callID] = delegation
		}
		infos = append(infos, cloneDelegationInfo(delegation.info))
	}
	return infos
}

func (r *subAgentRuntime) registerAliases(policy *ToolPolicy) {
	if policy == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for canonical, alias := range policy.Aliases {
		if _, ok := r.agents[canonical]; !ok {
			continue
		}
		for _, name := range alias.Names {
			r.toolToAgent[name] = canonical
		}
	}
}

func (r *subAgentRuntime) fail(callID, agentName string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delegation := r.byID[callID]
	if delegation == nil && callID == "" {
		for _, id := range r.pending[agentName] {
			if candidate := r.byID[id]; candidate != nil {
				delegation = candidate
				break
			}
		}
	}
	if delegation != nil {
		delegation.err = err
	}
}

func (r *subAgentRuntime) endInvocation(callID string, err error) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if delegation := r.byID[callID]; delegation != nil {
		delegation.inFlight = false
		delegation.err = err
	}
	if err == nil {
		return false
	}
	if _, interrupted := compose.IsInterruptRerunError(err); interrupted {
		return false
	}
	if _, interrupted := compose.ExtractInterruptInfo(err); interrupted {
		return false
	}
	return true
}

func (r *subAgentRuntime) emitTerminalEnd(callID string) {
	info, err, ok := r.finish(callID)
	if !ok {
		return
	}
	r.parent.emtr.Emit(Event{
		Type:       EventDelegationEnd,
		Agent:      info.Agent,
		Delegation: info,
		Error:      err,
	})
}

func (r *subAgentRuntime) finish(callID string) (*DelegationInfo, error, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delegation := r.byID[callID]
	if delegation == nil {
		return nil, nil, false
	}
	delete(r.byID, callID)
	if r.activeByAgent[delegation.info.Agent] == delegation {
		delete(r.activeByAgent, delegation.info.Agent)
	}
	info := cloneDelegationInfo(delegation.info)
	return &info, delegation.err, true
}

func (r *subAgentRuntime) hasAgent(name string) bool {
	if r == nil {
		return false
	}
	_, ok := r.agents[name]
	return ok
}

func (r *subAgentRuntime) delegationForAgent(name string, path []string) *DelegationInfo {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delegation := r.activeByAgent[name]
	if delegation == nil {
		return nil
	}
	info := cloneDelegationInfo(delegation.info)
	if len(path) > 0 {
		info.Path = append([]string(nil), path...)
	}
	return &info
}

func (r *subAgentRuntime) onlyActiveDelegation() *DelegationInfo {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var active *subAgentDelegation
	for _, delegation := range r.activeByAgent {
		if active != nil && active != delegation {
			return nil
		}
		active = delegation
	}
	if active == nil {
		return nil
	}
	info := cloneDelegationInfo(active.info)
	return &info
}

func (r *subAgentRuntime) resetBudget() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.delegations = 0
	r.byID = make(map[string]*subAgentDelegation)
	r.activeByAgent = make(map[string]*subAgentDelegation)
	r.pending = make(map[string][]string)
	r.nestedTools = make(map[string]toolCallInfo)
	r.nestedAgents = make(map[string]string)
	r.mu.Unlock()
}

func (r *subAgentRuntime) resetUsage() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.usage = nil
	r.mu.Unlock()
}

func (r *subAgentRuntime) addUsage(usage *TokenUsage) {
	if r == nil || usage == nil {
		return
	}
	r.mu.Lock()
	r.usage = addTokenUsage(r.usage, usage)
	r.mu.Unlock()
}

func (r *subAgentRuntime) runUsage() *TokenUsage {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.usage == nil {
		return nil
	}
	usage := *r.usage
	return &usage
}

func (r *subAgentRuntime) recordNestedTools(agentName string, calls []schema.ToolCall) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, call := range calls {
		if call.ID == "" {
			continue
		}
		key := nestedToolKey(agentName, call.ID)
		info := r.nestedTools[key]
		if info.start == nil {
			info.start = make(chan struct{})
		}
		info.name = call.Function.Name
		info.arguments = call.Function.Arguments
		if !info.started {
			close(info.start)
			info.started = true
		}
		r.nestedTools[key] = info
		r.nestedAgents[key] = agentName
	}
}

func (r *subAgentRuntime) waitNestedTool(ctx context.Context, agentName, callID string) (string, string, bool) {
	if r == nil || callID == "" {
		return "", "", true
	}
	key := nestedToolKey(agentName, callID)
	r.mu.Lock()
	info := r.nestedTools[key]
	if info.start == nil {
		info.start = make(chan struct{})
		r.nestedTools[key] = info
		r.nestedAgents[key] = agentName
	}
	start, started := info.start, info.started
	r.mu.Unlock()
	if !started {
		select {
		case <-start:
		case <-ctx.Done():
			return "", "", false
		}
	}
	r.mu.Lock()
	info = r.nestedTools[key]
	r.mu.Unlock()
	return info.name, info.arguments, true
}

func (r *subAgentRuntime) nestedToolInfo(agentName, callID string) (string, string) {
	if r == nil || callID == "" {
		return "", ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	info := r.nestedTools[nestedToolKey(agentName, callID)]
	return info.name, info.arguments
}

func (r *subAgentRuntime) clearNestedTool(agentName, callID string) bool {
	if r == nil || callID == "" {
		return true
	}
	key := nestedToolKey(agentName, callID)
	r.mu.Lock()
	delete(r.nestedTools, key)
	delete(r.nestedAgents, key)
	remaining := false
	for _, name := range r.nestedAgents {
		if name == agentName {
			remaining = true
			break
		}
	}
	r.mu.Unlock()
	return !remaining
}

func nestedToolKey(agentName, callID string) string {
	return agentName + "\x00" + callID
}

func (a *Agent) delegationForAgent(name string, path []string) *DelegationInfo {
	if a == nil || a.subAgents == nil {
		return nil
	}
	return a.subAgents.delegationForAgent(name, path)
}

func eventRunPath(event *adk.AgentEvent) []string {
	if event == nil || len(event.RunPath) == 0 {
		return nil
	}
	path := make([]string, len(event.RunPath))
	for index := range event.RunPath {
		path[index] = event.RunPath[index].String()
	}
	return path
}

func cloneDelegationInfo(info DelegationInfo) DelegationInfo {
	info.Path = append([]string(nil), info.Path...)
	return info
}

func subAgentToolContextMiddleware(parent *Agent, agentName string) compose.ToolMiddleware {
	withContext := func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, emitterCtxKey{}, parent.emtr)
		ctx = context.WithValue(ctx, agentNameCtxKey{}, agentName)
		return context.WithValue(ctx, agentCtxKey{}, parent)
	}
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				return next(withContext(ctx), input)
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				return next(withContext(ctx), input)
			}
		},
		EnhancedInvokable: func(next compose.EnhancedInvokableToolEndpoint) compose.EnhancedInvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
				return next(withContext(ctx), input)
			}
		},
		EnhancedStreamable: func(next compose.EnhancedStreamableToolEndpoint) compose.EnhancedStreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
				return next(withContext(ctx), input)
			}
		},
	}
}
