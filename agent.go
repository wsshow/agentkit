package agentkit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

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
	if e != nil {
		e.Emit(Event{Type: EventToolUpdate, Agent: name, Content: content})
	}
}

// Config Agent 配置
type Config struct {
	Name                string
	Description         string
	SystemPrompt        string
	Model               ChatModel                  // 聊天模型（可直接使用 agentkit.ChatModel 别名）
	Tools               []Tool                     // 工具列表（可直接使用 agentkit.Tool 别名）
	Handlers            []ChatModelAgentMiddleware // ChatModelAgent 扩展处理器
	ModelRetryConfig    *ModelRetryConfig          // 模型调用重试配置（可选）
	ModelFailoverConfig *ModelFailoverConfig       // 模型失败转移配置（可选）
	MaxIterations       int                        // 默认 20
	CheckPointStore     compose.CheckPointStore    // 自定义 CheckPoint 存储，默认使用内存存储
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

	history       []*schema.Message // 完整对话历史（含 assistant/tool），用于 steering/follow-up 重放
	steeringQueue []Message
	followUpQueue []Message
	steeringMode  QueueMode
	followUpMode  QueueMode
}

// New 创建 Agent
func New(ctx context.Context, cfg *Config) (*Agent, error) {
	maxIter := cfg.MaxIterations
	if maxIter == 0 {
		maxIter = 20
	}

	desc := cfg.Description
	if desc == "" {
		desc = cfg.Name
	}

	agentCfg := &adk.ChatModelAgentConfig{
		Name:                cfg.Name,
		Description:         desc,
		Instruction:         cfg.SystemPrompt,
		Model:               cfg.Model,
		MaxIterations:       maxIter,
		Handlers:            cfg.Handlers,
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

	return &Agent{
		name:         cfg.Name,
		runner:       runner,
		state:        newState(),
		emtr:         newEmitter(),
		checkPointID: cfg.Name + "/" + uuid.New().String(),
		steeringMode: QueueModeOneAtATime,
		followUpMode: QueueModeOneAtATime,
	}, nil
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
	return a.run(ctx)
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
	return a.run(ctx)
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
	return a.run(ctx)
}

// Steer 在 Agent 执行期间插入转向消息。
// 工具结果返回后检查队列，若有消息则中断当前执行并注入新消息。
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

	ctx = context.WithValue(context.WithValue(ctx, emitterCtxKey{}, a.emtr), agentNameCtxKey{}, a.name)

	a.emtr.Emit(Event{Type: EventAgentStart, Agent: a.name})

	err := a.executeResume(ctx, targets)
	err = a.processQueues(ctx, err)

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
	out := make([]*schema.Message, len(a.history))
	copy(out, a.history)
	return out
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
