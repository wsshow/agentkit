package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

// ErrMockModelNoResponse 表示没有可消费的预设响应。
var ErrMockModelNoResponse = errors.New("mock chat model has no response configured")

// MockModelCall 记录一次模型调用。
type MockModelCall struct {
	Input     []*schema.Message
	Options   []model.Option
	Streaming bool
}

// MockModelExpectation 校验一次模型调用。
type MockModelExpectation func(call MockModelCall) error

// MockModelResponse 描述一次模型返回。
type MockModelResponse struct {
	Message   *schema.Message
	Chunks    []*schema.Message
	Err       error
	StreamErr error
	Expect    MockModelExpectation
	Build     func(call MockModelCall) MockModelResponse
}

// MockChatModel 按顺序返回预设响应，并记录每次调用。
type MockChatModel struct {
	mu        sync.Mutex
	responses []MockModelResponse
	calls     []MockModelCall
}

// MockToolProvider 提供可加入 Agent 配置的工具。
type MockToolProvider interface {
	MockTool() Tool
}

// MockToolCallProvider 提供模型发起的工具调用。
type MockToolCallProvider interface {
	MockToolCall() schema.ToolCall
}

// MockTool 描述一个可在测试中执行的工具。
type MockTool[T, D any] struct {
	Tool Tool
	Name string
}

// MockToolInvocation 描述一次由模型发起并由工具执行的调用。
type MockToolInvocation[D any] struct {
	Tool      Tool
	CallID    string
	Name      string
	Arguments string
}

// NewMockTool 创建可复用的工具。
func NewMockTool[T, D any](name, desc string, fn func(context.Context, T) (D, error)) (*MockTool[T, D], error) {
	t, err := utils.InferTool(name, desc, utils.InvokeFunc[T, D](fn))
	if err != nil {
		return nil, err
	}
	return &MockTool[T, D]{
		Tool: t,
		Name: name,
	}, nil
}

// MustMockTool 创建可复用的工具。
func MustMockTool[T, D any](name, desc string, fn func(context.Context, T) (D, error)) *MockTool[T, D] {
	toolFunction, err := NewMockTool(name, desc, fn)
	if err != nil {
		panic(err)
	}
	return toolFunction
}

// MockToolCallFunc 创建带实际执行函数的工具调用。
func MockToolCallFunc[T, D any](name, desc string, input T, fn func(context.Context, T) (D, error)) (*MockToolInvocation[D], error) {
	toolFunction, err := NewMockTool(name, desc, fn)
	if err != nil {
		return nil, err
	}
	return toolFunction.Invocation(defaultMockToolCallID(name), input)
}

// MustMockToolCallFunc 创建带实际执行函数的工具调用。
func MustMockToolCallFunc[T, D any](name, desc string, input T, fn func(context.Context, T) (D, error)) *MockToolInvocation[D] {
	invocation, err := MockToolCallFunc(name, desc, input, fn)
	if err != nil {
		panic(err)
	}
	return invocation
}

// MockTools 提取工具调用中的工具列表。
func MockTools(items ...MockToolProvider) []Tool {
	tools := make([]Tool, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		if item == nil {
			continue
		}
		t := item.MockTool()
		if t == nil {
			continue
		}
		if info, err := t.Info(context.Background()); err == nil && info != nil && info.Name != "" {
			if _, ok := seen[info.Name]; ok {
				continue
			}
			seen[info.Name] = struct{}{}
		}
		tools = append(tools, t)
	}
	return tools
}

// Invocation 创建一次工具调用。
func (f *MockTool[T, D]) Invocation(callID string, input T) (*MockToolInvocation[D], error) {
	if f == nil {
		return nil, nil
	}
	if callID == "" {
		callID = defaultMockToolCallID(f.Name)
	}
	arguments, err := mockMarshalToolInput(input)
	if err != nil {
		return nil, err
	}
	return &MockToolInvocation[D]{
		Tool:      f.Tool,
		CallID:    callID,
		Name:      f.Name,
		Arguments: arguments,
	}, nil
}

// MustInvocation 创建一次工具调用。
func (f *MockTool[T, D]) MustInvocation(callID string, input T) *MockToolInvocation[D] {
	invocation, err := f.Invocation(callID, input)
	if err != nil {
		panic(err)
	}
	return invocation
}

// Call 创建一次工具调用。
func (f *MockTool[T, D]) Call(callID string, input T) *MockToolInvocation[D] {
	return f.MustInvocation(callID, input)
}

// MockTool 返回工具函数对应的工具。
func (f *MockTool[T, D]) MockTool() Tool {
	if f == nil {
		return nil
	}
	return f.Tool
}

// MockTool 返回工具调用对应的工具。
func (i *MockToolInvocation[D]) MockTool() Tool {
	if i == nil {
		return nil
	}
	return i.Tool
}

// MockToolCall 返回模型发起的工具调用。
func (i *MockToolInvocation[D]) MockToolCall() schema.ToolCall {
	if i == nil {
		return schema.ToolCall{}
	}
	return schema.ToolCall{
		ID:   i.CallID,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      i.Name,
			Arguments: i.Arguments,
		},
	}
}

// NewMockChatModel 创建按顺序返回响应的模型。
func NewMockChatModel(responses ...MockModelResponse) *MockChatModel {
	m := &MockChatModel{}
	m.AddResponses(responses...)
	return m
}

// AddResponses 追加后续响应。
func (m *MockChatModel) AddResponses(responses ...MockModelResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, response := range responses {
		m.responses = append(m.responses, cloneMockResponse(response))
	}
}

// Calls 返回模型调用记录。
func (m *MockChatModel) Calls() []MockModelCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneMockCalls(m.calls)
}

// LastCall 返回最近一次模型调用记录。
func (m *MockChatModel) LastCall() (MockModelCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return MockModelCall{}, false
	}
	return cloneMockCall(m.calls[len(m.calls)-1]), true
}

// RemainingResponses 返回还未消费的响应数量。
func (m *MockChatModel) RemainingResponses() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.responses)
}

// Generate 返回下一条完整响应。
func (m *MockChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	response, err := m.nextResponse(input, opts, false)
	if err != nil {
		return nil, err
	}
	if response.Err != nil {
		return nil, response.Err
	}
	if response.Message != nil {
		return cloneSchemaMessage(response.Message), nil
	}
	if len(response.Chunks) > 0 {
		return schema.ConcatMessages(cloneSchemaMessages(response.Chunks))
	}
	return schema.AssistantMessage("", nil), nil
}

// Stream 返回下一条流式响应。
func (m *MockChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	response, err := m.nextResponse(input, opts, true)
	if err != nil {
		return nil, err
	}
	if response.Err != nil {
		return nil, response.Err
	}

	chunks := cloneSchemaMessages(response.Chunks)
	if len(chunks) == 0 {
		if response.Message != nil {
			chunks = []*schema.Message{cloneSchemaMessage(response.Message)}
		} else {
			chunks = []*schema.Message{schema.AssistantMessage("", nil)}
		}
	}

	if response.StreamErr == nil {
		return schema.StreamReaderFromArray(chunks), nil
	}

	reader, writer := schema.Pipe[*schema.Message](len(chunks) + 1)
	go func() {
		defer writer.Close()
		for _, chunk := range chunks {
			select {
			case <-ctx.Done():
				writer.Send(nil, ctx.Err())
				return
			default:
			}
			if writer.Send(chunk, nil) {
				return
			}
		}
		writer.Send(nil, response.StreamErr)
	}()
	return reader, nil
}

func (m *MockChatModel) nextResponse(input []*schema.Message, opts []model.Option, streaming bool) (MockModelResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	call := MockModelCall{
		Input:     cloneSchemaMessages(input),
		Options:   append([]model.Option(nil), opts...),
		Streaming: streaming,
	}
	m.calls = append(m.calls, call)

	if len(m.responses) == 0 {
		return MockModelResponse{}, ErrMockModelNoResponse
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	if response.Expect != nil {
		if err := response.Expect(cloneMockCall(call)); err != nil {
			return MockModelResponse{}, err
		}
	}
	if response.Build != nil {
		built := cloneMockResponse(response.Build(cloneMockCall(call)))
		built.Expect = response.Expect
		response = built
	}
	return cloneMockResponse(response), nil
}

// MockModelText 创建文本回复。
func MockModelText(content string) MockModelResponse {
	return MockModelMessage(schema.AssistantMessage(content, nil))
}

// MockModelReasoning 创建带推理内容的文本回复。
func MockModelReasoning(content, reasoning string) MockModelResponse {
	return MockModelMessage(&schema.Message{
		Role:             schema.Assistant,
		Content:          content,
		ReasoningContent: reasoning,
	})
}

// MockModelMessage 创建完整消息回复。
func MockModelMessage(message *schema.Message) MockModelResponse {
	return MockModelResponse{Message: cloneSchemaMessage(message)}
}

// MockModelStream 创建文本分片回复。
func MockModelStream(chunks ...string) MockModelResponse {
	messages := make([]*schema.Message, 0, len(chunks))
	for _, chunk := range chunks {
		messages = append(messages, &schema.Message{
			Role:    schema.Assistant,
			Content: chunk,
		})
	}
	return MockModelResponse{Chunks: messages}
}

// MockModelToolCall 创建单个工具调用回复。
func MockModelToolCall(name, arguments string) MockModelResponse {
	return MockModelToolCallWithID(defaultMockToolCallID(name), name, arguments)
}

// MockModelToolCallWithID 创建指定调用 ID 的工具调用回复。
func MockModelToolCallWithID(id, name, arguments string) MockModelResponse {
	if id == "" {
		id = defaultMockToolCallID(name)
	}
	return MockModelToolCalls(schema.ToolCall{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	})
}

// MockModelCalls 创建一组工具调用回复。
func MockModelCalls(invocations ...MockToolCallProvider) MockModelResponse {
	calls := make([]schema.ToolCall, 0, len(invocations))
	for _, invocation := range invocations {
		if invocation == nil {
			continue
		}
		calls = append(calls, invocation.MockToolCall())
	}
	return MockModelToolCalls(calls...)
}

// MockModelCallsAfter 在指定工具结果返回后创建下一组工具调用回复。
func MockModelCallsAfter(wait MockToolCallProvider, calls ...MockToolCallProvider) MockModelResponse {
	return MockModelCallsAfterAll([]MockToolCallProvider{wait}, calls...)
}

// MockModelCallsAfterAll 在指定工具结果全部返回后创建下一组工具调用回复。
func MockModelCallsAfterAll(wait []MockToolCallProvider, calls ...MockToolCallProvider) MockModelResponse {
	return MockModelAfterToolResults(MockModelCalls(calls...), mockToolCallIDs(wait...)...)
}

// MockModelRespondsAfter 根据工具结果生成回复。
func MockModelRespondsAfter[D any](call *MockToolInvocation[D], reply func(D) MockModelResponse) MockModelResponse {
	if call == nil {
		return MockModelError(errors.New("mock tool invocation is nil"))
	}
	return MockModelRespondsAfterToolResultAs(call.CallID, reply)
}

// MockModelTextAfter 根据工具结果生成文本回复。
func MockModelTextAfter[D any](call *MockToolInvocation[D], reply func(D) string) MockModelResponse {
	return MockModelRespondsAfter(call, func(result D) MockModelResponse {
		if reply == nil {
			return MockModelText("")
		}
		return MockModelText(reply(result))
	})
}

// MockModelTextAfterAll 在指定工具结果全部返回后创建文本回复。
func MockModelTextAfterAll(content string, calls ...MockToolCallProvider) MockModelResponse {
	return MockModelAfterToolResults(MockModelText(content), mockToolCallIDs(calls...)...)
}

// MockModelAfterToolResult 要求输入中包含指定工具结果后再返回响应。
func MockModelAfterToolResult(callID string, response MockModelResponse) MockModelResponse {
	return MockModelAfterToolResults(response, callID)
}

// MockModelTextAfterToolResult 使用工具结果作为文本回复。
func MockModelTextAfterToolResult(callID string) MockModelResponse {
	return MockModelRespondsAfterToolResult(callID, func(result string) MockModelResponse {
		return MockModelText(result)
	})
}

// MockModelRespondsAfterToolResult 根据工具结果生成响应。
func MockModelRespondsAfterToolResult(callID string, reply func(result string) MockModelResponse) MockModelResponse {
	response := MockExpect(MockModelResponse{}, func(call MockModelCall) error {
		_, ok := mockInputToolResult(call.Input, callID)
		if !ok {
			return fmt.Errorf("mock chat model expected tool result for call ID %q", callID)
		}
		return nil
	})
	response.Build = func(call MockModelCall) MockModelResponse {
		result, _ := mockInputToolResult(call.Input, callID)
		if reply == nil {
			return MockModelText(result)
		}
		return reply(result)
	}
	return response
}

// MockModelRespondsAfterToolResultAs 根据工具结果生成回复。
func MockModelRespondsAfterToolResultAs[D any](callID string, reply func(D) MockModelResponse) MockModelResponse {
	response := MockExpect(MockModelResponse{}, func(call MockModelCall) error {
		result, ok := mockInputToolResult(call.Input, callID)
		if !ok {
			return fmt.Errorf("mock chat model expected tool result for call ID %q", callID)
		}
		_, err := mockDecodeToolResult[D](result)
		return err
	})
	response.Build = func(call MockModelCall) MockModelResponse {
		result, _ := mockInputToolResult(call.Input, callID)
		decoded, err := mockDecodeToolResult[D](result)
		if err != nil {
			return MockModelError(err)
		}
		if reply == nil {
			return MockModelText(result)
		}
		return reply(decoded)
	}
	return response
}

// MockModelAfterToolResults 要求输入中包含指定工具结果后再返回响应。
func MockModelAfterToolResults(response MockModelResponse, callIDs ...string) MockModelResponse {
	return MockExpect(response, MockExpectToolResults(callIDs...))
}

// MockExpect 为响应追加调用校验。
func MockExpect(response MockModelResponse, expect MockModelExpectation) MockModelResponse {
	if expect == nil {
		return response
	}
	prev := response.Expect
	response.Expect = func(call MockModelCall) error {
		if prev != nil {
			if err := prev(call); err != nil {
				return err
			}
		}
		return expect(call)
	}
	return response
}

// MockExpectToolResults 校验输入中存在指定工具结果。
func MockExpectToolResults(callIDs ...string) MockModelExpectation {
	return func(call MockModelCall) error {
		missing := make([]string, 0, len(callIDs))
		for _, callID := range callIDs {
			if !mockInputHasToolResult(call.Input, callID) {
				missing = append(missing, callID)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("mock chat model expected tool results for call IDs %v", missing)
		}
		return nil
	}
}

// MockModelToolCalls 创建多个工具调用回复。
func MockModelToolCalls(calls ...schema.ToolCall) MockModelResponse {
	out := cloneToolCalls(calls)
	for i := range out {
		if out[i].ID == "" {
			out[i].ID = fmt.Sprintf("call_%d", i+1)
		}
		if out[i].Type == "" {
			out[i].Type = "function"
		}
	}
	return MockModelMessage(schema.AssistantMessage("", out))
}

// MockModelError 创建调用错误。
func MockModelError(err error) MockModelResponse {
	return MockModelResponse{Err: err}
}

// MockModelStreamError 创建分片后的流式错误。
func MockModelStreamError(err error, chunks ...string) MockModelResponse {
	response := MockModelStream(chunks...)
	response.StreamErr = err
	return response
}

func cloneMockCalls(calls []MockModelCall) []MockModelCall {
	out := make([]MockModelCall, len(calls))
	for i := range calls {
		out[i] = cloneMockCall(calls[i])
	}
	return out
}

func cloneMockCall(call MockModelCall) MockModelCall {
	return MockModelCall{
		Input:     cloneSchemaMessages(call.Input),
		Options:   append([]model.Option(nil), call.Options...),
		Streaming: call.Streaming,
	}
}

func cloneMockResponse(response MockModelResponse) MockModelResponse {
	return MockModelResponse{
		Message:   cloneSchemaMessage(response.Message),
		Chunks:    cloneSchemaMessages(response.Chunks),
		Err:       response.Err,
		StreamErr: response.StreamErr,
		Expect:    response.Expect,
		Build:     response.Build,
	}
}

func cloneSchemaMessages(messages []*schema.Message) []*schema.Message {
	return cloneHistoryMessages(messages)
}

func cloneSchemaMessage(msg *schema.Message) *schema.Message {
	return cloneHistoryMessage(msg)
}

func mockInputHasToolResult(input []*schema.Message, callID string) bool {
	_, ok := mockInputToolResult(input, callID)
	return ok
}

func mockInputToolResult(input []*schema.Message, callID string) (string, bool) {
	for _, msg := range input {
		if msg != nil && msg.Role == schema.Tool && msg.ToolCallID == callID {
			return msg.Content, true
		}
	}
	return "", false
}

func mockToolCallIDs(calls ...MockToolCallProvider) []string {
	ids := make([]string, 0, len(calls))
	for _, call := range calls {
		if call == nil {
			continue
		}
		toolCall := call.MockToolCall()
		if toolCall.ID != "" {
			ids = append(ids, toolCall.ID)
		}
	}
	return ids
}

func defaultMockToolCallID(name string) string {
	if name == "" {
		return "call_1"
	}
	return fmt.Sprintf("call_%s", name)
}

func mockMarshalToolInput(input any) (string, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func mockDecodeToolResult[D any](result string) (D, error) {
	var out D
	if s, ok := any(&out).(*string); ok {
		*s = result
		return out, nil
	}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		return out, err
	}
	return out, nil
}
