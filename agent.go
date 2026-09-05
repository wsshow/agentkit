package agentkit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

// QueueMode 消息队列处理模式
type QueueMode string

const (
	QueueModeOneAtATime QueueMode = "one-at-a-time" // 每次处理一条
	QueueModeAll        QueueMode = "all"           // 一次性处理全部
)

type emitterCtxKey struct{}
type agentNameCtxKey struct{}
type agentCtxKey struct{}

var (
	// ErrAgentClosed 表示 Agent 已关闭，不能再执行新的请求。
	ErrAgentClosed = errors.New("agentkit: agent is closed")
	// ErrAgentRunning 表示 Agent 正在执行另一个请求。
	ErrAgentRunning = errors.New("agent is already running")
	// ErrNoMessagesToContinue 表示 Agent 没有可继续执行的历史消息。
	ErrNoMessagesToContinue = errors.New("no messages in state to continue from")
	// ErrCannotContinue 表示最后一条消息已由助手完成，不能继续执行。
	ErrCannotContinue = errors.New("cannot continue from assistant message, last message must be user or tool result")
	// ErrResumeRequired 表示存在未处理的检查点，必须先 Resume 或 ClearCheckpoint。
	ErrResumeRequired = errors.New("agentkit: pending checkpoint must be resumed or cleared before starting a new run")
)

// EmitToolUpdate 在工具执行中发送进度更新事件。
// 工具通过 context 获取 Emitter 并发送 tool_update 事件：
//
//	func myTool(ctx context.Context, input string) (string, error) {
//	    agentkit.EmitToolUpdate(ctx, "正在处理...")
//	    return "done", nil
//	}
func EmitToolUpdate(ctx context.Context, content string) {
	e, _ := ctx.Value(emitterCtxKey{}).(*emitter)
	name, _ := ctx.Value(agentNameCtxKey{}).(string)
	callID := compose.GetToolCallID(ctx)
	toolName, arguments := "", ""
	if a, _ := ctx.Value(agentCtxKey{}).(*Agent); a != nil {
		var ok bool
		toolName, arguments, ok = a.waitToolCallInfo(ctx, callID)
		if !ok {
			return
		}
	}
	if e != nil {
		e.Emit(Event{
			Type:          EventToolUpdate,
			Agent:         name,
			Content:       content,
			ToolCallID:    callID,
			ToolName:      toolName,
			ToolArguments: arguments,
		})
	}
}

// Config Agent 配置
type Config struct {
	Name                string
	Description         string
	SystemPrompt        string
	Model               ChatModel                  // 聊天模型（可直接使用 agentkit.ChatModel 别名）
	Tools               []Tool                     // 工具列表（可直接使用 agentkit.Tool 别名）
	ToolPolicy          *ToolPolicy                // 工具别名、分发、执行顺序与中间件（可选）
	History             []*schema.Message          // 完整对话历史（可选）
	Handlers            []ChatModelAgentMiddleware // ChatModelAgent 扩展处理器
	ModelRetryConfig    *ModelRetryConfig          // 模型调用重试配置（可选）
	ModelFailoverConfig *ModelFailoverConfig       // 模型失败转移配置（可选）
	MaxIterations       int                        // 默认 20
	CheckPointStore     compose.CheckPointStore    // 自定义 CheckPoint 存储；默认使用 Session 配套存储或内存存储
	Session             *SessionConfig             // 自动恢复并保存完整对话（可选）
	Compaction          *CompactionConfig          // 自动上下文压缩（可选）
	Skills              *SkillsConfig              // 按需加载 SKILL.md（可选）
	MCP                 *MCPConfig                 // 自动连接并管理 MCP 服务器（可选）
}

// Agent 提供事件流驱动的交互能力。
type Agent struct {
	name            string
	runner          *adk.Runner
	state           *State
	emtr            *emitter
	checkPointID    string // 每个 Agent 实例唯一的 CheckPoint ID
	checkpointStore CheckpointStore

	mu       sync.Mutex
	cancelFn context.CancelFunc
	running  bool          // 是否正在执行
	closed   bool          // 是否已关闭
	done     chan struct{} // 执行完成信号
	inTurn   atomic.Bool   // turn 状态跟踪（原子操作，线程安全）

	history                  []*schema.Message // 完整对话历史（含 assistant/tool），用于展示和持久化
	contextHistory           []*schema.Message // 发送给模型的上下文，压缩前与 history 相同
	contextCompacted         bool
	steeringQueue            []Message
	followUpQueue            []Message
	steeringMode             QueueMode
	followUpMode             QueueMode
	toolCalls                map[string]toolCallInfo
	toolBatchDone            chan struct{}
	toolBatchDoneFlag        bool
	compactionMessagesBefore int
	pendingInterrupts        []InterruptPoint
	runInterrupted           bool
	knownToolNames           map[string]struct{}

	sessionStore     SessionStore
	sessionID        string
	sessionCreatedAt time.Time
	sessionUpdatedAt time.Time
	sessionSaveMu    sync.Mutex

	mcpConnections []managedMCPConnection
	mcpCloseOnce   sync.Once
	mcpCloseErr    error
}

type toolCallInfo struct {
	name      string
	arguments string
	start     chan struct{}
	started   bool
}

// New 创建 Agent
func New(ctx context.Context, cfg *Config) (*Agent, error) {
	if err := validateConfig(ctx, cfg); err != nil {
		return nil, err
	}

	history := cfg.History
	contextHistory := history
	var loadedSession *Session
	if cfg.Session != nil {
		var err error
		loadedSession, err = cfg.Session.Store.Load(ctx, cfg.Session.ID)
		if errors.Is(err, ErrSessionNotFound) {
			now := time.Now().UTC()
			loadedSession = &Session{ID: cfg.Session.ID, CreatedAt: now, UpdatedAt: now}
		} else if err != nil {
			return nil, fmt.Errorf("agentkit: load session %q: %w", cfg.Session.ID, err)
		}
		history = loadedSession.Messages
		if loadedSession.Context != nil {
			contextHistory = loadedSession.Context
		} else {
			contextHistory = history
		}
	}

	maxIter := cfg.MaxIterations
	if maxIter == 0 {
		maxIter = 20
	}

	desc := cfg.Description
	if desc == "" {
		desc = cfg.Name
	}

	checkPointID := cfg.Name + "/" + uuid.New().String()
	if loadedSession != nil {
		checkPointID = loadedSession.CheckpointID
		if checkPointID == "" {
			checkPointID = "agentkit/session/" + sessionStorageKey(cfg.Name+"\x00"+loadedSession.ID)
		}
	}

	a := &Agent{
		name:         cfg.Name,
		state:        newState(),
		emtr:         newEmitter(),
		checkPointID: checkPointID,
		steeringMode: QueueModeOneAtATime,
		followUpMode: QueueModeOneAtATime,
		toolCalls:    make(map[string]toolCallInfo),
	}
	if loadedSession != nil {
		a.sessionStore = cfg.Session.Store
		a.sessionID = loadedSession.ID
		a.sessionCreatedAt = loadedSession.CreatedAt
		a.sessionUpdatedAt = loadedSession.UpdatedAt
	}
	a.restoreHistory(history, contextHistory, loadedSession != nil && loadedSession.Context != nil)
	if loadedSession != nil {
		a.pendingInterrupts = cloneInterruptPoints(loadedSession.PendingInterrupts)
	}

	handlers := make([]ChatModelAgentMiddleware, 0, len(cfg.Handlers)+3)
	handlers = append(handlers, cfg.Handlers...)
	if cfg.Skills != nil {
		middleware, err := newSkillsMiddleware(ctx, cfg.Skills)
		if err != nil {
			return nil, err
		}
		handlers = append(handlers, middleware)
	}
	if cfg.Compaction != nil {
		middleware, err := newCompactionMiddleware(ctx, a, cfg.Model, cfg.Compaction)
		if err != nil {
			return nil, err
		}
		handlers = append(handlers, middleware)
	}
	handlers = append(handlers, &agentLifecycleHandler{
		BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
		agent:                        a,
	})

	tools := append([]Tool(nil), cfg.Tools...)
	var mcpConnections []managedMCPConnection
	if cfg.MCP != nil {
		mcpTools, connections, err := connectMCP(ctx, cfg.MCP)
		if err != nil {
			return nil, err
		}
		mcpConnections = connections
		tools = append(tools, mcpTools...)
	}
	if err := validateCombinedToolNames(ctx, tools, cfg.Skills); err != nil {
		return nil, errors.Join(err, closeMCPConnections(mcpConnections))
	}
	knownToolNames, err := validateToolPolicy(ctx, tools, cfg.Skills, cfg.ToolPolicy)
	if err != nil {
		return nil, errors.Join(err, closeMCPConnections(mcpConnections))
	}
	a.knownToolNames = knownToolNames

	agentCfg := &adk.ChatModelAgentConfig{
		Name:                cfg.Name,
		Description:         desc,
		Instruction:         cfg.SystemPrompt,
		Model:               cfg.Model,
		MaxIterations:       maxIter,
		Handlers:            handlers,
		ModelRetryConfig:    cfg.ModelRetryConfig,
		ModelFailoverConfig: cfg.ModelFailoverConfig,
	}

	if len(tools) > 0 || cfg.ToolPolicy != nil {
		agentCfg.ToolsConfig = adk.ToolsConfig{
			ToolsNodeConfig: cfg.ToolPolicy.toolsNodeConfig(tools),
		}
	}

	adkAgent, err := adk.NewChatModelAgent(ctx, agentCfg)
	if err != nil {
		return nil, errors.Join(err, closeMCPConnections(mcpConnections))
	}

	store := cfg.CheckPointStore
	if store == nil && cfg.Session != nil {
		if provider, ok := cfg.Session.Store.(CheckpointStoreProvider); ok {
			store = provider.CheckpointStore()
		}
	}
	if store == nil {
		store = NewMemoryCheckpointStore()
	}
	a.checkpointStore = store

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           adkAgent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	a.runner = runner
	a.mcpConnections = mcpConnections
	return a, nil
}

func validateConfig(ctx context.Context, cfg *Config) error {
	if ctx == nil {
		return errors.New("agentkit: context is required")
	}
	if cfg == nil {
		return errors.New("agentkit: config is required")
	}
	if cfg.Model == nil {
		return errors.New("agentkit: model is required")
	}
	if cfg.MaxIterations < 0 {
		return fmt.Errorf("agentkit: max iterations must not be negative: %d", cfg.MaxIterations)
	}
	if cfg.Session != nil {
		if cfg.Session.ID == "" {
			return errors.New("agentkit: session ID is required")
		}
		if cfg.Session.Store == nil {
			return errors.New("agentkit: session store is required")
		}
		if cfg.History != nil {
			return errors.New("agentkit: history and session cannot be configured together")
		}
	}
	if err := validateCompactionConfig(cfg.Compaction); err != nil {
		return err
	}
	if err := validateSkillsConfig(cfg.Skills); err != nil {
		return err
	}
	if err := validateMCPConfig(cfg.MCP); err != nil {
		return err
	}
	return nil
}

// Prompt 发送用户输入并驱动 Agent 执行，事件通过 Subscribe 订阅。
// 如果 Agent 已在执行中，返回错误。
func (a *Agent) Prompt(ctx context.Context, input string) error {
	_, err := a.promptWithResult(ctx, input)
	return err
}

// Ask 发送用户文本并返回本次执行的完整结果。
func (a *Agent) Ask(ctx context.Context, input string) (*RunResult, error) {
	return a.promptWithResult(ctx, input)
}

func (a *Agent) promptWithResult(ctx context.Context, input string) (*RunResult, error) {
	runCtx, err := a.startFreshRun(ctx)
	if err != nil {
		return nil, err
	}
	defer a.endRun()
	return a.executePromptWithResult(runCtx, input)
}

func (a *Agent) executePromptWithResult(runCtx context.Context, input string) (*RunResult, error) {
	a.mu.Lock()
	historyOffset := len(a.history)
	a.mu.Unlock()
	a.state.AddMessage(Message{Role: RoleUser, Content: input})
	a.appendHistory(schema.UserMessage(input))
	err := a.run(runCtx, []Message{{Role: RoleUser, Content: input}})
	return a.resultSince(historyOffset), err
}

// Send 发送多模态内容并驱动 Agent 执行。
// 使用 Text、ImageURL、AudioURL 等构造函数创建 ContentPart。
// 如果 Agent 已在执行中，返回错误。
func (a *Agent) Send(ctx context.Context, parts ...ContentPart) error {
	_, err := a.sendWithResult(ctx, parts...)
	return err
}

// AskParts 发送多模态内容并返回本次执行的完整结果。
func (a *Agent) AskParts(ctx context.Context, parts ...ContentPart) (*RunResult, error) {
	return a.sendWithResult(ctx, parts...)
}

func (a *Agent) sendWithResult(ctx context.Context, parts ...ContentPart) (*RunResult, error) {
	runCtx, err := a.startFreshRun(ctx)
	if err != nil {
		return nil, err
	}
	defer a.endRun()
	return a.executeSendWithResult(runCtx, parts...)
}

func (a *Agent) executeSendWithResult(runCtx context.Context, parts ...ContentPart) (*RunResult, error) {
	a.mu.Lock()
	historyOffset := len(a.history)
	a.mu.Unlock()
	// 提取纯文本用于 State 记录
	var textContent string
	for _, p := range parts {
		if p.Type == schema.ChatMessagePartTypeText {
			textContent += p.Text
		}
	}
	a.state.AddMessage(Message{Role: RoleUser, Content: textContent})
	a.appendHistory(&schema.Message{
		Role:                  schema.User,
		UserInputMultiContent: parts,
	})
	err := a.run(runCtx, []Message{{Role: RoleUser, Content: textContent}})
	return a.resultSince(historyOffset), err
}

// Continue 从当前状态恢复执行（不添加新消息），用于错误后重试。
// 如果 Agent 已在执行中，返回错误。
func (a *Agent) Continue(ctx context.Context) error {
	_, err := a.continueWithResult(ctx)
	return err
}

// ContinueWithResult 从当前状态继续执行并返回本次新增的结果。
func (a *Agent) ContinueWithResult(ctx context.Context) (*RunResult, error) {
	return a.continueWithResult(ctx)
}

func (a *Agent) continueWithResult(ctx context.Context) (*RunResult, error) {
	runCtx, err := a.startFreshRun(ctx)
	if err != nil {
		return nil, err
	}
	defer a.endRun()

	a.mu.Lock()
	histLen := len(a.history)
	var lastRole schema.RoleType
	if histLen > 0 {
		lastRole = a.history[histLen-1].Role
	}
	a.mu.Unlock()

	if histLen == 0 {
		return nil, ErrNoMessagesToContinue
	}
	if lastRole == schema.Assistant {
		return nil, ErrCannotContinue
	}
	err = a.run(runCtx, nil)
	return a.resultSince(histLen), err
}

// Steer 在 Agent 执行期间插入转向消息。
// 当前工具批次完成后检查队列，若有消息则中断当前执行并注入新消息。
func (a *Agent) Steer(content string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steeringQueue = append(a.steeringQueue, Message{Role: RoleUser, Content: content})
}

// FollowUp 在 Agent 完成当前工作后追加后续消息。
// 只有在没有转向消息时才会被处理。
func (a *Agent) FollowUp(content string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.followUpQueue = append(a.followUpQueue, Message{Role: RoleUser, Content: content})
}

// SetSteeringMode 设置转向消息处理模式
func (a *Agent) SetSteeringMode(mode QueueMode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steeringMode = mode
}

// SetFollowUpMode 设置后续消息处理模式
func (a *Agent) SetFollowUpMode(mode QueueMode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.followUpMode = mode
}

// ClearSteeringQueue 清空转向消息队列
func (a *Agent) ClearSteeringQueue() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steeringQueue = nil
}

// ClearFollowUpQueue 清空后续消息队列
func (a *Agent) ClearFollowUpQueue() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.followUpQueue = nil
}

// ClearAllQueues 清空所有消息队列
func (a *Agent) ClearAllQueues() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steeringQueue = nil
	a.followUpQueue = nil
}

func (a *Agent) drainSteering() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.steeringQueue) == 0 {
		return nil
	}
	if a.steeringMode == QueueModeOneAtATime {
		msg := a.steeringQueue[0]
		a.steeringQueue = a.steeringQueue[1:]
		return []Message{msg}
	}
	msgs := a.steeringQueue
	a.steeringQueue = nil
	return msgs
}

func (a *Agent) drainFollowUp() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.followUpQueue) == 0 {
		return nil
	}
	if a.followUpMode == QueueModeOneAtATime {
		msg := a.followUpQueue[0]
		a.followUpQueue = a.followUpQueue[1:]
		return []Message{msg}
	}
	msgs := a.followUpQueue
	a.followUpQueue = nil
	return msgs
}

func (a *Agent) appendHistory(msg *schema.Message) {
	msg = cloneHistoryMessage(msg)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = append(a.history, msg)
	a.contextHistory = append(a.contextHistory, msg)
}

// Resume 从 HITL 中断恢复执行。
// targets 格式为 map[interruptID]data，interruptID 来自 Event.Interrupt[].ID。
// 如果 Agent 已在执行中，返回错误。
func (a *Agent) Resume(ctx context.Context, targets map[string]any) error {
	_, err := a.resumeWithResult(ctx, targets)
	return err
}

// ResumeWithResult 从 HITL 中断恢复并返回本次恢复执行新增的结果。
func (a *Agent) ResumeWithResult(ctx context.Context, targets map[string]any) (*RunResult, error) {
	return a.resumeWithResult(ctx, targets)
}

func (a *Agent) resumeWithResult(ctx context.Context, targets map[string]any) (*RunResult, error) {
	runCtx, err := a.startRun(ctx)
	if err != nil {
		return nil, err
	}
	defer a.endRun()
	a.mu.Lock()
	historyOffset := len(a.history)
	a.mu.Unlock()

	runCtx = a.withRunContext(runCtx)

	a.emtr.Emit(Event{Type: EventAgentStart, Agent: a.name})

	err = a.executeResume(runCtx, targets)
	err = a.processQueues(runCtx, err)
	if err == nil && !a.wasInterrupted() {
		if cleanupErr := a.discardCheckpoint(runCtx); cleanupErr != nil {
			a.emtr.Emit(Event{Type: EventError, Agent: a.name, Error: cleanupErr})
			err = cleanupErr
		}
	}
	err = a.persistSession(runCtx, err)

	a.emtr.Emit(Event{Type: EventAgentEnd, Agent: a.name})
	return a.resultSince(historyOffset), err
}

// Subscribe 订阅事件流，返回取消订阅函数。
// 回调按订阅顺序同步执行，每个回调收到独立的事件快照；nil 回调会被忽略。
func (a *Agent) Subscribe(fn Subscriber) func() {
	return a.emtr.Subscribe(fn)
}

// Cancel 请求取消当前执行且不等待完成。
// 可在 Subscribe 回调中安全调用；需要等待执行退出时请在回调外使用 Abort。
func (a *Agent) Cancel() {
	a.mu.Lock()
	cancel := a.cancelFn
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Abort 取消当前执行并等待完成。
// Subscribe 回调与执行处于同一 goroutine，回调内请使用 Cancel 以避免等待自身。
func (a *Agent) Abort() {
	a.mu.Lock()
	cancel := a.cancelFn
	done := a.done
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// Close 关闭 Agent，释放资源。实现 io.Closer 接口。
func (a *Agent) Close() error {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	a.Abort()
	a.mcpCloseOnce.Do(func() {
		a.mcpCloseErr = closeMCPConnections(a.mcpConnections)
	})
	return a.mcpCloseErr
}

// State 获取当前状态
func (a *Agent) State() *State {
	return a.state
}

// Name 获取 Agent 名称
func (a *Agent) Name() string {
	return a.name
}

// History 获取完整对话历史（含 assistant/tool 的 schema.Message），用于调试或持久化。
func (a *Agent) History() []*schema.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneHistoryMessages(a.history)
}

// ContextHistory 获取当前发送给模型的上下文副本。
// 未发生压缩时，它与 History 相同；压缩后 History 仍保留完整对话。
func (a *Agent) ContextHistory() []*schema.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneHistoryMessages(a.contextHistory)
}

// Session 获取当前会话快照。未配置会话持久化时返回 nil。
func (a *Agent) Session() *Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionStore == nil {
		return nil
	}
	return &Session{
		ID:                a.sessionID,
		CreatedAt:         a.sessionCreatedAt,
		UpdatedAt:         a.sessionUpdatedAt,
		Messages:          cloneHistoryMessages(a.history),
		Context:           a.sessionContextLocked(),
		CheckpointID:      a.checkPointID,
		PendingInterrupts: cloneInterruptPoints(a.pendingInterrupts),
	}
}

// SaveSession 立即保存当前会话快照。
// Prompt、Send、Continue 和 Resume 结束时会自动调用它。
func (a *Agent) SaveSession(ctx context.Context) error {
	if ctx == nil {
		return errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.sessionSaveMu.Lock()
	defer a.sessionSaveMu.Unlock()

	a.mu.Lock()
	if a.sessionStore == nil {
		a.mu.Unlock()
		return ErrSessionDisabled
	}
	now := time.Now().UTC()
	createdAt := a.sessionCreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	session := &Session{
		ID:                a.sessionID,
		CreatedAt:         createdAt,
		UpdatedAt:         now,
		Messages:          cloneHistoryMessages(a.history),
		Context:           a.sessionContextLocked(),
		CheckpointID:      a.checkPointID,
		PendingInterrupts: cloneInterruptPoints(a.pendingInterrupts),
	}
	store := a.sessionStore
	a.mu.Unlock()

	if err := store.Save(ctx, session); err != nil {
		return fmt.Errorf("agentkit: save session %q: %w", session.ID, err)
	}

	a.mu.Lock()
	a.sessionCreatedAt = createdAt
	a.sessionUpdatedAt = now
	a.mu.Unlock()
	return nil
}

// SetHistory 替换完整对话历史，并同步展示状态。
func (a *Agent) SetHistory(history []*schema.Message) {
	a.Abort()
	_ = a.discardCheckpoint(context.Background())
	a.replaceHistory(history)
}

// Reset 重置 Agent 状态（清空消息历史和队列）。
// 如果 Agent 正在执行，先等待执行完成。
func (a *Agent) Reset() {
	a.Abort()
	_ = a.discardCheckpoint(context.Background())
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Clear()
	a.history = nil
	a.contextHistory = nil
	a.contextCompacted = false
	a.steeringQueue = nil
	a.followUpQueue = nil
	a.toolCalls = make(map[string]toolCallInfo)
	a.toolBatchDone = nil
	a.toolBatchDoneFlag = false
	a.compactionMessagesBefore = 0
}

// PendingInterrupts 返回当前等待 Resume 的中断点副本。
func (a *Agent) PendingInterrupts() []InterruptPoint {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneInterruptPoints(a.pendingInterrupts)
}

// ClearCheckpoint 放弃当前中断并使已有检查点失效。
// 配置了 Session 时，清理后的状态会立即持久化。
func (a *Agent) ClearCheckpoint(ctx context.Context) error {
	if ctx == nil {
		return errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.Abort()
	err := a.discardCheckpoint(ctx)
	if a.sessionStore != nil {
		err = errors.Join(err, a.SaveSession(ctx))
	}
	return err
}

func (a *Agent) startFreshRun(ctx context.Context) (context.Context, error) {
	runCtx, err := a.startRun(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.ensureNoPendingCheckpoint(runCtx); err != nil {
		a.endRun()
		return nil, err
	}
	return runCtx, nil
}

// startRun 标记 Agent 开始执行。如果已在执行中，返回错误。
func (a *Agent) startRun(ctx context.Context) (context.Context, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, ErrAgentClosed
	}
	if a.running {
		return nil, ErrAgentRunning
	}
	if ctx == nil {
		return nil, errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	a.running = true
	a.runInterrupted = false
	a.done = make(chan struct{})
	a.cancelFn = cancel
	return runCtx, nil
}

// endRun 标记 Agent 执行完成。
func (a *Agent) endRun() {
	a.mu.Lock()
	cancel := a.cancelFn
	a.cancelFn = nil
	a.running = false
	if a.done != nil {
		close(a.done)
		a.done = nil
	}
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
