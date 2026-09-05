package agentkit

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// processSubAgentEvent publishes a child Agent's internal activity without
// appending it to the parent Agent's state or conversation history.
func (a *Agent) processSubAgentEvent(ctx context.Context, event *adk.AgentEvent) error {
	agentName := event.AgentName
	path := eventRunPath(event)

	if event.Err != nil {
		a.emitDelegatedEvent(agentName, path, Event{Type: EventError, Error: event.Err})
		return nil
	}
	if event.Action != nil {
		a.processSubAgentAction(agentName, path, event.Action)
	}
	if event.Output == nil || event.Output.MessageOutput == nil {
		return nil
	}
	output := event.Output.MessageOutput
	if output.IsStreaming && output.MessageStream != nil {
		return a.processSubAgentStream(ctx, agentName, path, output.MessageStream)
	}
	if output.Message != nil {
		a.processSubAgentMessage(agentName, path, output.Message)
	}
	return nil
}

func (a *Agent) processSubAgentAction(agentName string, path []string, action *adk.AgentAction) {
	if action.TransferToAgent != nil {
		a.emitDelegatedEvent(agentName, path, Event{
			Type:    EventTransfer,
			Content: action.TransferToAgent.DestAgentName,
		})
	}
	// AgentTool turns child interruptions into a composite parent interruption,
	// so this branch is mainly defensive for custom Agent implementations.
	if action.Interrupted != nil {
		points := make([]InterruptPoint, 0, len(action.Interrupted.InterruptContexts))
		for _, item := range action.Interrupted.InterruptContexts {
			points = append(points, InterruptPoint{ID: item.ID, Info: item.Info})
		}
		a.emitDelegatedEvent(agentName, path, Event{Type: EventInterrupted, Interrupt: points})
	}
}

func (a *Agent) processSubAgentMessage(agentName string, path []string, msg adk.Message) {
	if msg.Role == schema.Tool {
		toolName, arguments := a.subAgents.nestedToolInfo(agentName, msg.ToolCallID)
		if msg.ToolName != "" {
			toolName = msg.ToolName
		}
		a.emitDelegatedEvent(agentName, path, Event{
			Type:          EventToolEnd,
			Content:       msg.Content,
			ToolCallID:    msg.ToolCallID,
			ToolName:      toolName,
			ToolArguments: arguments,
		})
		a.emitDelegatedEvent(agentName, path, Event{Type: EventMessageStart, Role: RoleTool, Content: msg.Content})
		a.emitDelegatedEvent(agentName, path, Event{Type: EventMessageEnd, Role: RoleTool, Content: msg.Content})
		if a.subAgents.clearNestedTool(agentName, msg.ToolCallID) {
			a.emitDelegatedEvent(agentName, path, Event{Type: EventTurnEnd})
		}
		return
	}

	a.emitDelegatedEvent(agentName, path, Event{Type: EventTurnStart})
	a.emitDelegatedEvent(agentName, path, Event{Type: EventMessageStart, Role: RoleAssistant, Content: msg.Content})
	a.emitDelegatedEvent(agentName, path, Event{
		Type:             EventMessageEnd,
		Role:             RoleAssistant,
		Content:          msg.Content,
		ReasoningContent: msg.ReasoningContent,
		ResponseMeta:     msg.ResponseMeta,
	})
	if msg.ResponseMeta != nil {
		a.subAgents.addUsage(msg.ResponseMeta.Usage)
	}
	if len(msg.ToolCalls) == 0 {
		a.emitDelegatedEvent(agentName, path, Event{Type: EventTurnEnd})
		return
	}
	a.subAgents.recordNestedTools(agentName, msg.ToolCalls)
	a.emitDelegatedEvent(agentName, path, Event{Type: EventToolStart, ToolCalls: msg.ToolCalls})
}

func (a *Agent) processSubAgentStream(ctx context.Context, agentName string, path []string, stream adk.MessageStream) error {
	a.emitDelegatedEvent(agentName, path, Event{Type: EventTurnStart})
	a.emitDelegatedEvent(agentName, path, Event{Type: EventMessageStart, Role: RoleAssistant})

	var content strings.Builder
	var reasoning strings.Builder
	var toolCalls []schema.ToolCall
	var responseMeta *schema.ResponseMeta
	for {
		select {
		case <-ctx.Done():
			a.emitDelegatedEvent(agentName, path, Event{Type: EventError, Error: ctx.Err()})
			return ctx.Err()
		default:
		}

		chunk, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			a.emitDelegatedEvent(agentName, path, Event{Type: EventError, Error: err})
			return err
		}
		if chunk == nil {
			continue
		}
		if chunk.ReasoningContent != "" {
			reasoning.WriteString(chunk.ReasoningContent)
			a.emitDelegatedEvent(agentName, path, Event{Type: EventReasoningDelta, Delta: chunk.ReasoningContent})
		}
		if chunk.Content != "" {
			content.WriteString(chunk.Content)
			a.emitDelegatedEvent(agentName, path, Event{Type: EventMessageDelta, Delta: chunk.Content})
		}
		if chunk.ResponseMeta != nil {
			responseMeta = chunk.ResponseMeta
		}
		if len(chunk.ToolCalls) > 0 {
			toolCalls = mergeToolCalls(toolCalls, chunk.ToolCalls)
		}
	}

	a.emitDelegatedEvent(agentName, path, Event{
		Type:             EventMessageEnd,
		Role:             RoleAssistant,
		Content:          content.String(),
		ReasoningContent: reasoning.String(),
		ResponseMeta:     responseMeta,
	})
	if responseMeta != nil {
		a.subAgents.addUsage(responseMeta.Usage)
	}
	if len(toolCalls) == 0 {
		a.emitDelegatedEvent(agentName, path, Event{Type: EventTurnEnd})
		return nil
	}
	a.subAgents.recordNestedTools(agentName, toolCalls)
	a.emitDelegatedEvent(agentName, path, Event{Type: EventToolStart, ToolCalls: toolCalls})
	return nil
}

func (a *Agent) emitDelegatedEvent(agentName string, path []string, event Event) {
	event.Agent = agentName
	event.Delegation = a.delegationForAgent(agentName, path)
	a.emtr.Emit(event)
}
