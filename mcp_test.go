package agentkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	einotool "github.com/cloudwego/eino/components/tool"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestValidateMCPConfig(t *testing.T) {
	session := newFakeMCPClientSession("tool")
	tests := []struct {
		name string
		cfg  *MCPConfig
		want string
	}{
		{name: "no servers", cfg: &MCPConfig{}, want: "at least one server"},
		{name: "negative keep alive", cfg: &MCPConfig{KeepAlive: -1, Servers: []MCPServerConfig{{Name: "test", Session: session}}}, want: "keep alive"},
		{name: "invalid result limit", cfg: &MCPConfig{MaxResultChars: -2, Servers: []MCPServerConfig{{Name: "test", Session: session}}}, want: "max result chars"},
		{name: "missing name", cfg: &MCPConfig{Servers: []MCPServerConfig{{Session: session}}}, want: "name is required"},
		{name: "duplicate server", cfg: &MCPConfig{Servers: []MCPServerConfig{{Name: "same", Session: session}, {Name: "same", Session: session}}}, want: "duplicate MCP server name"},
		{name: "missing transport", cfg: &MCPConfig{Servers: []MCPServerConfig{{Name: "test"}}}, want: "transport is required"},
		{name: "stdio command", cfg: &MCPConfig{Servers: []MCPServerConfig{{Name: "test", Transport: MCPTransportStdio}}}, want: "stdio command is required"},
		{name: "stdio HTTP settings", cfg: &MCPConfig{Servers: []MCPServerConfig{{Name: "test", Transport: MCPTransportStdio, Command: "server", URL: "https://example.com"}}}, want: "does not accept URL"},
		{name: "relative HTTP URL", cfg: &MCPConfig{Servers: []MCPServerConfig{{Name: "test", Transport: MCPTransportStreamableHTTP, URL: "/mcp"}}}, want: "must be absolute"},
		{name: "HTTP command settings", cfg: &MCPConfig{Servers: []MCPServerConfig{{Name: "test", Transport: MCPTransportSSE, URL: "https://example.com", Command: "server"}}}, want: "does not accept command"},
		{name: "session and transport", cfg: &MCPConfig{Servers: []MCPServerConfig{{Name: "test", Session: session, Transport: MCPTransportSSE}}}, want: "cannot be configured together"},
		{name: "duplicate filter", cfg: &MCPConfig{Servers: []MCPServerConfig{{Name: "test", Session: session, ToolNames: []string{"tool", "tool"}}}}, want: "duplicate tool filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateMCPConfig(tt.cfg); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateMCPConfig() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestConnectMCPDiscoversAllPagesAndPrefixesFilteredTools(t *testing.T) {
	beta := fakeMCPTool("beta")
	beta.Description = strings.Repeat("description ", 500)
	session := &fakeMCPClientSession{pages: map[string]*protocol.ListToolsResult{
		"":     {Tools: []*protocol.Tool{fakeMCPTool("alpha")}, NextCursor: "next"},
		"next": {Tools: []*protocol.Tool{beta}},
	}}
	cfg := &MCPConfig{Servers: []MCPServerConfig{{
		Name:       "remote",
		Session:    session,
		ToolNames:  []string{"beta"},
		ToolPrefix: "remote__",
	}}}
	if err := validateMCPConfig(cfg); err != nil {
		t.Fatal(err)
	}
	tools, connections, err := connectMCP(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connectMCP() error = %v", err)
	}
	defer closeMCPConnections(connections)
	if len(tools) != 1 {
		t.Fatalf("tools length = %d, want 1", len(tools))
	}
	info, err := tools[0].Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "remote__beta" {
		t.Fatalf("tool name = %q, want remote__beta", info.Name)
	}
	if utf8.RuneCountInString(info.Desc) > DefaultMCPMaxDescriptionChars {
		t.Fatalf("tool description length = %d, want <= %d", utf8.RuneCountInString(info.Desc), DefaultMCPMaxDescriptionChars)
	}
	if got := session.listCursors(); len(got) != 2 || got[0] != "" || got[1] != "next" {
		t.Fatalf("list cursors = %#v, want [\"\", \"next\"]", got)
	}
}

func TestConnectMCPRejectsMissingFilteredToolAndClosesSession(t *testing.T) {
	session := newFakeMCPClientSession("available")
	cfg := &MCPConfig{Servers: []MCPServerConfig{{
		Name:      "remote",
		Session:   session,
		ToolNames: []string{"available", "missing"},
	}}}
	if err := validateMCPConfig(cfg); err != nil {
		t.Fatal(err)
	}
	_, _, err := connectMCP(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), `does not provide requested tool "missing"`) {
		t.Fatalf("connectMCP() error = %v, want missing filtered tool", err)
	}
	if got := session.closeCount(); got != 1 {
		t.Fatalf("close count = %d, want 1", got)
	}
}

func TestConnectMCPClosesEverySessionAfterPartialFailure(t *testing.T) {
	first := newFakeMCPClientSession("first")
	second := newFakeMCPClientSession("second")
	second.listErr = errors.New("list failed")
	cfg := &MCPConfig{Servers: []MCPServerConfig{
		{Name: "first", Session: first},
		{Name: "second", Session: second},
	}}
	if err := validateMCPConfig(cfg); err != nil {
		t.Fatal(err)
	}
	_, _, err := connectMCP(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "list failed") {
		t.Fatalf("connectMCP() error = %v, want list failure", err)
	}
	if first.closeCount() != 1 || second.closeCount() != 1 {
		t.Fatalf("close counts = %d, %d; want 1, 1", first.closeCount(), second.closeCount())
	}
}

func TestConnectMCPTruncatesLargeToolResults(t *testing.T) {
	session := newFakeMCPClientSession("large")
	session.result = &protocol.CallToolResult{
		Content: []protocol.Content{&protocol.TextContent{Text: strings.Repeat("数据", 1_000)}},
	}
	cfg := &MCPConfig{
		MaxResultChars: 400,
		Servers:        []MCPServerConfig{{Name: "remote", Session: session}},
	}
	if err := validateMCPConfig(cfg); err != nil {
		t.Fatal(err)
	}
	tools, connections, err := connectMCP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeMCPConnections(connections)
	result, err := tools[0].(einotool.InvokableTool).InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if utf8.RuneCountInString(result) > 400 || !strings.Contains(result, "truncated") {
		t.Fatalf("truncated result length = %d, result = %q", utf8.RuneCountInString(result), result)
	}
}

func TestAgentCallsStreamableHTTPMCPToolAndClosesConnection(t *testing.T) {
	type addInput struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	server := protocol.NewServer(&protocol.Implementation{Name: "math", Version: "v1.0.0"}, nil)
	protocol.AddTool(server, &protocol.Tool{Name: "add", Description: "add two integers"}, func(_ context.Context, _ *protocol.CallToolRequest, input addInput) (*protocol.CallToolResult, any, error) {
		return &protocol.CallToolResult{Content: []protocol.Content{&protocol.TextContent{Text: fmt.Sprint(input.X + input.Y)}}}, nil, nil
	})
	baseHandler := protocol.NewStreamableHTTPHandler(func(*http.Request) *protocol.Server { return server }, nil)
	var sawAuthorization atomic.Bool
	var deleteCount atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") == "Bearer test" {
			sawAuthorization.Store(true)
		}
		if request.Method == http.MethodDelete {
			deleteCount.Add(1)
		}
		baseHandler.ServeHTTP(writer, request)
	}))
	defer httpServer.Close()

	const callID = "mcp-add-call"
	model := NewMockChatModel(
		MockModelToolCallWithID(callID, "math__add", `{"x":2,"y":3}`),
		MockModelTextAfterToolResult(callID),
	)
	agent, err := New(context.Background(), &Config{
		Name:  "assistant",
		Model: model,
		MCP: &MCPConfig{Servers: []MCPServerConfig{{
			Name:       "math",
			Transport:  MCPTransportStreamableHTTP,
			URL:        httpServer.URL,
			Headers:    map[string]string{"Authorization": "Bearer test"},
			ToolPrefix: "math__",
		}}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	events := newMockEventRecorder()
	agent.Subscribe(events.Record)
	if err := agent.Prompt(context.Background(), "add two and three"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	end := events.Last(EventToolEnd)
	if end == nil || end.ToolName != "math__add" || !strings.Contains(end.Content, `"text":"5"`) {
		t.Fatalf("tool end event = %#v", end)
	}
	if !sawAuthorization.Load() {
		t.Fatal("MCP requests did not include configured Authorization header")
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	closed := deleteCount.Load()
	if closed == 0 {
		t.Fatal("Agent.Close() did not close the MCP HTTP session")
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if deleteCount.Load() != closed {
		t.Fatalf("second Close() sent another DELETE: before=%d after=%d", closed, deleteCount.Load())
	}
}

func TestAgentRejectsDuplicateMCPToolNamesAndClosesConnections(t *testing.T) {
	first := newFakeMCPClientSession("same")
	second := newFakeMCPClientSession("same")
	_, err := New(context.Background(), &Config{
		Model: NewMockChatModel(),
		MCP: &MCPConfig{Servers: []MCPServerConfig{
			{Name: "first", Session: first},
			{Name: "second", Session: second},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate tool name "same"`) {
		t.Fatalf("New() error = %v, want duplicate tool name", err)
	}
	if first.closeCount() != 1 || second.closeCount() != 1 {
		t.Fatalf("close counts = %d, %d; want 1, 1", first.closeCount(), second.closeCount())
	}
}

func TestAgentCloseReturnsMCPErrorOnlyOnce(t *testing.T) {
	closeErr := errors.New("close failed")
	session := newFakeMCPClientSession("tool")
	session.closeErr = closeErr
	agent, err := New(context.Background(), &Config{
		Model: NewMockChatModel(),
		MCP:   &MCPConfig{Servers: []MCPServerConfig{{Name: "remote", Session: session}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 16
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- agent.Close()
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, closeErr) {
			t.Fatalf("Close() error = %v, want close error", err)
		}
	}
	if got := session.closeCount(); got != 1 {
		t.Fatalf("close count = %d, want 1", got)
	}
}

func TestAgentCloseContextBoundsMCPShutdown(t *testing.T) {
	session := &blockingMCPClientSession{
		fakeMCPClientSession: newFakeMCPClientSession("tool"),
		started:              make(chan struct{}),
		release:              make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(session.release) }) }
	defer release()
	agent, err := New(context.Background(), &Config{
		Model: NewMockChatModel(),
		MCP:   &MCPConfig{Servers: []MCPServerConfig{{Name: "remote", Session: session}}},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	closeCtx, cancelClose := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = agent.CloseContext(closeCtx)
	cancelClose()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext() error = %v, want context.DeadlineExceeded", err)
	}
	select {
	case <-session.started:
	default:
		t.Fatal("MCP close did not start")
	}
	if err := agent.Prompt(context.Background(), "closed"); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("Prompt() error = %v, want ErrAgentClosed", err)
	}

	release()
	if err := agent.CloseContext(context.Background()); err != nil {
		t.Fatalf("second CloseContext() error = %v", err)
	}
	if got := session.closeCount(); got != 1 {
		t.Fatalf("MCP close count = %d, want 1", got)
	}
}

type fakeMCPClientSession struct {
	mu          sync.Mutex
	pages       map[string]*protocol.ListToolsResult
	listErr     error
	result      *protocol.CallToolResult
	callErr     error
	cursors     []string
	calledTools []string
	closes      int
	closeErr    error
}

type blockingMCPClientSession struct {
	*fakeMCPClientSession
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingMCPClientSession) Close() error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return s.fakeMCPClientSession.Close()
}

func newFakeMCPClientSession(names ...string) *fakeMCPClientSession {
	tools := make([]*protocol.Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, fakeMCPTool(name))
	}
	return &fakeMCPClientSession{pages: map[string]*protocol.ListToolsResult{"": {Tools: tools}}}
}

func fakeMCPTool(name string) *protocol.Tool {
	return &protocol.Tool{
		Name:        name,
		Description: name + " description",
		InputSchema: map[string]any{"type": "object"},
	}
}

func (s *fakeMCPClientSession) ListTools(_ context.Context, params *protocol.ListToolsParams) (*protocol.ListToolsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	cursor := ""
	if params != nil {
		cursor = params.Cursor
	}
	s.cursors = append(s.cursors, cursor)
	page := s.pages[cursor]
	if page == nil {
		return &protocol.ListToolsResult{}, nil
	}
	return page, nil
}

func (s *fakeMCPClientSession) CallTool(_ context.Context, params *protocol.CallToolParams) (*protocol.CallToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.callErr != nil {
		return nil, s.callErr
	}
	if params != nil {
		s.calledTools = append(s.calledTools, params.Name)
	}
	if s.result != nil {
		return s.result, nil
	}
	return &protocol.CallToolResult{Content: []protocol.Content{&protocol.TextContent{Text: "ok"}}}, nil
}

func (s *fakeMCPClientSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
	return s.closeErr
}

func (s *fakeMCPClientSession) listCursors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cursors...)
}

func (s *fakeMCPClientSession) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}
