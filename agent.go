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
	History             []*schema.Message          // 完整对话历史（可选）
	Handlers            []ChatModelAgentMiddleware // ChatModelAgent 扩展处理器
	ModelRetryConfig    *ModelRetryConfig          // 模型调用重试配置（可选）
	ModelFailoverConfig *ModelFailoverConfig       // 模型失败转移配置（可选）
	MaxIterations       int                        // 默认 20
	CheckPointStore     compose.CheckPointStore    // 自定义 CheckPoint 存储，默认使用内存存储
	Session             *SessionConfig             // 自动恢复并保存完整对话（可选）
}

// Agent 提供事件流驱动的交互能力。
type Agent struct {
	name         string
	runner       *adk.Runner
	state        *State
	emtr         *emitter
	checkPointID string // 每个 Agent 实例唯一的 CheckPoint ID

	mu       sync.Mutex
	cancelFn context.CancelFunc
	running  bool          // 是否正在执行
	done     chan struct{} // 执行完成信号
	inTurn   atomic.Bool   // turn 状态跟踪（原子操作，线程安全）

	history           []*schema.Message // 完整对话历史（含 assistant/tool），用于 steering/follow-up 重放
	steeringQueue     []Message
	followUpQueue     []Message
	steeringMode      QueueMode
	followUpMode      QueueMode
	toolCalls         map[string]toolCallInfo
	toolBatchDone     chan struct{}
	toolBatchDoneFlag bool

	sessionStore     SessionStore
	sessionID        string
	sessionCreatedAt time.Time
	sessionUpdatedAt time.Time
	sessionSaveMu    sync.Mutex
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
		checkPointID = "agentkit/session/" + sessionStorageKey(cfg.Name+"\x00"+loadedSession.ID)
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
	a.replaceHistory(history)

	handlers := make([]ChatModelAgentMiddleware, 0, len(cfg.Handlers)+1)
	handlers = append(handlers, cfg.Handlers...)
	handlers = append(handlers, &agentLifecycleHandler{
		BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
		agent:                        a,
	})

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

	if len(cfg.Tools) > 0 {
		agentCfg.ToolsConfig = adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: cfg.Tools,
			},
		}
	}

	adkAgent, err := adk.NewChatModelAgent(ctx, agentCfg)
	if err != nil {
		return nil, err
	}

	store := cfg.CheckPointStore
	if store == nil {
		store = newInMemoryStore()
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           adkAgent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	a.runner = runner
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
	return nil
}

// Prompt 发送用户输入并驱动 Agent 执行，事件通过 Subscribe 订阅。
// 如果 Agent 已在执行中，返回错误。
func (a *Agent) Prompt(ctx context.Context, input string) error {
	if err := a.startRun(); err != nil {
		return err
	}
	defer a.endRun()

	a.state.AddMessage(Message{Role: RoleUser, Content: input})
	a.appendHistory(schema.UserMessage(input))
	return a.run(ctx, []Message{{Role: RoleUser, Content: input}})
}

// Send 发送多模态内容并驱动 Agent 执行。
// 使用 Text、ImageURL、AudioURL 等构造函数创建 ContentPart。
// 如果 Agent 已在执行中，返回错误。
func (a *Agent) Send(ctx context.Context, parts ...ContentPart) error {
	if err := a.startRun(); err != nil {
		return err
	}
	defer a.endRun()

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
	return a.run(ctx, []Message{{Role: RoleUser, Content: textContent}})
}

// Continue 从当前状态恢复执行（不添加新消息），用于错误后重试。
// 如果 Agent 已在执行中，返回错误。
func (a *Agent) Continue(ctx context.Context) error {
	if err := a.startRun(); err != nil {
		return err
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
		return errors.New("no messages in state to continue from")
	}
	if lastRole == schema.Assistant {
		return errors.New("cannot continue from assistant message, last message must be user or tool result")
	}
	return a.run(ctx, nil)
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
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = append(a.history, msg)
}

// Resume 从 HITL 中断恢复执行。
// targets 格式为 map[interruptID]data，interruptID 来自 Event.Interrupt[].ID。
// 如果 Agent 已在执行中，返回错误。
func (a *Agent) Resume(ctx context.Context, targets map[string]any) error {
	if err := a.startRun(); err != nil {
		return err
	}
	defer a.endRun()

	ctx = a.withRunContext(ctx)

	a.emtr.Emit(Event{Type: EventAgentStart, Agent: a.name})

	err := a.executeResume(ctx, targets)
	err = a.processQueues(ctx, err)
	err = a.persistSession(ctx, err)

	a.emtr.Emit(Event{Type: EventAgentEnd, Agent: a.name})
	return err
}

// Subscribe 订阅事件流，返回取消订阅函数
func (a *Agent) Subscribe(fn Subscriber) func() {
	return a.emtr.Subscribe(fn)
}

// Abort 取消当前执行并等待完成
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
	a.Abort()
	return nil
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

// Session 获取当前会话快照。未配置会话持久化时返回 nil。
func (a *Agent) Session() *Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionStore == nil {
		return nil
	}
	return &Session{
		ID:        a.sessionID,
		CreatedAt: a.sessionCreatedAt,
		UpdatedAt: a.sessionUpdatedAt,
		Messages:  cloneHistoryMessages(a.history),
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
		ID:        a.sessionID,
		CreatedAt: createdAt,
		UpdatedAt: now,
		Messages:  cloneHistoryMessages(a.history),
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
	a.replaceHistory(history)
}

// Reset 重置 Agent 状态（清空消息历史和队列）。
// 如果 Agent 正在执行，先等待执行完成。
func (a *Agent) Reset() {
	a.Abort()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Clear()
	a.history = nil
	a.steeringQueue = nil
	a.followUpQueue = nil
	a.toolCalls = make(map[string]toolCallInfo)
	a.toolBatchDone = nil
	a.toolBatchDoneFlag = false
}

// startRun 标记 Agent 开始执行。如果已在执行中，返回错误。
func (a *Agent) startRun() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return errors.New("agent is already running")
	}
	a.running = true
	a.done = make(chan struct{})
	return nil
}

// endRun 标记 Agent 执行完成。
func (a *Agent) endRun() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = false
	if a.done != nil {
		close(a.done)
		a.done = nil
	}
}
