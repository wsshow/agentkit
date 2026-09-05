package agentkit

import (
	"context"
	"errors"
	"sync"
)

// RunStream 表示一次正在执行的流式请求。
// Events 会按 Agent 的事件顺序输出并在队列排空后关闭；Wait 不依赖事件消费进度。
type RunStream struct {
	events <-chan Event
	done   chan struct{}
	cancel context.CancelFunc
	broker *runEventBroker

	mu     sync.Mutex
	result *RunResult
	err    error
}

// Events 返回本次请求专属的事件流。
func (s *RunStream) Events() <-chan Event {
	if s == nil {
		return nil
	}
	return s.events
}

// Done 在本次请求完成时关闭，不要求调用方先消费完 Events。
func (s *RunStream) Done() <-chan struct{} {
	if s == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return s.done
}

// Wait 等待请求完成并返回隔离的运行结果。
// Wait 可安全地重复调用；它不负责消费 Events。
func (s *RunStream) Wait() (*RunResult, error) {
	return s.WaitContext(context.Background())
}

// WaitContext 等待请求完成或 ctx 结束；等待超时不会取消底层执行。
// 可在超时后再次调用 Wait 或 WaitContext 获取最终结果。
func (s *RunStream) WaitContext(ctx context.Context) (*RunResult, error) {
	if s == nil {
		return nil, nil
	}
	if ctx == nil {
		return nil, errors.New("agentkit: context is required")
	}
	select {
	case <-s.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneRunResult(s.result), s.err
}

// Cancel 请求取消本次执行且不等待退出。
func (s *RunStream) Cancel() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}

// Close 取消本次执行并放弃尚未消费的事件。
// Wait 仍可用于等待底层执行结束并取得部分结果。
func (s *RunStream) Close() error {
	if s == nil {
		return nil
	}
	s.Cancel()
	s.broker.discardEvents()
	return nil
}

func (s *RunStream) complete(result *RunResult, err error) {
	s.mu.Lock()
	s.result = cloneRunResult(result)
	s.err = err
	s.mu.Unlock()
	close(s.done)
}

// Stream 发送文本并立即返回本次请求专属的事件流。
func (a *Agent) Stream(ctx context.Context, input string) (*RunStream, error) {
	runCtx, err := a.startFreshRun(ctx)
	if err != nil {
		return nil, err
	}
	return a.startStream(runCtx, func(ctx context.Context) (*RunResult, error) {
		return a.executePromptWithResult(ctx, input)
	}), nil
}

// StreamParts 发送多模态内容并立即返回本次请求专属的事件流。
func (a *Agent) StreamParts(ctx context.Context, parts ...ContentPart) (*RunStream, error) {
	runCtx, err := a.startFreshRun(ctx)
	if err != nil {
		return nil, err
	}
	clonedParts := cloneMessageInputParts(parts)
	return a.startStream(runCtx, func(ctx context.Context) (*RunResult, error) {
		return a.executeSendWithResult(ctx, clonedParts...)
	}), nil
}

func (a *Agent) startStream(runCtx context.Context, execute func(context.Context) (*RunResult, error)) *RunStream {
	streamCtx, cancel := context.WithCancel(runCtx)
	broker := newRunEventBroker()
	stream := &RunStream{
		events: broker.events,
		done:   make(chan struct{}),
		cancel: cancel,
		broker: broker,
	}
	unsubscribe := a.Subscribe(broker.emit)

	go func() {
		result, err := execute(streamCtx)
		unsubscribe()
		broker.finishEvents()
		a.endRun()
		stream.complete(result, err)
	}()
	return stream
}

type runEventBroker struct {
	in       chan Event
	events   chan Event
	stop     chan struct{}
	mu       sync.RWMutex
	finished bool
	finish   sync.Once
	discard  sync.Once
}

func newRunEventBroker() *runEventBroker {
	b := &runEventBroker{
		in:     make(chan Event, 32),
		events: make(chan Event),
		stop:   make(chan struct{}),
	}
	go b.run()
	return b
}

func (b *runEventBroker) emit(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.finished {
		return
	}
	select {
	case b.in <- event:
	case <-b.stop:
	}
}

func (b *runEventBroker) finishEvents() {
	b.finish.Do(func() {
		b.mu.Lock()
		b.finished = true
		close(b.in)
		b.mu.Unlock()
	})
}

func (b *runEventBroker) discardEvents() {
	b.discard.Do(func() { close(b.stop) })
}

func (b *runEventBroker) run() {
	defer close(b.events)
	queue := make([]Event, 0, 32)
	in := b.in
	for in != nil || len(queue) > 0 {
		var output chan Event
		var next Event
		if len(queue) > 0 {
			output = b.events
			next = queue[0]
		}
		select {
		case event, ok := <-in:
			if !ok {
				in = nil
				continue
			}
			queue = append(queue, event)
		case output <- next:
			queue[0] = Event{}
			queue = queue[1:]
		case <-b.stop:
			return
		}
	}
}

func cloneRunResult(result *RunResult) *RunResult {
	if result == nil {
		return nil
	}
	cloned := *result
	cloned.Response = cloneHistoryMessage(result.Response)
	cloned.Messages = cloneHistoryMessages(result.Messages)
	cloned.ToolCalls = cloneToolCalls(result.ToolCalls)
	cloned.Interrupts = cloneInterruptPoints(result.Interrupts)
	if result.Usage != nil {
		usage := *result.Usage
		cloned.Usage = &usage
	}
	return &cloned
}
