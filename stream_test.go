package agentkit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStreamReturnsOrderedRequestEventsAndResult(t *testing.T) {
	ctx := context.Background()
	agent, err := New(ctx, &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelStream("hel", "lo")),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	stream, err := agent.Stream(ctx, "say hello")
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	var types []EventType
	var text strings.Builder
	for event := range stream.Events() {
		types = append(types, event.Type)
		if event.Type == EventMessageDelta {
			text.WriteString(event.Delta)
		}
	}
	result, err := stream.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if text.String() != "hello" || result.Text != "hello" {
		t.Fatalf("streamed text = %q, result text = %q", text.String(), result.Text)
	}
	if len(types) == 0 || types[0] != EventAgentStart || types[len(types)-1] != EventAgentEnd {
		t.Fatalf("event types = %v", types)
	}
	secondResult, secondErr := stream.Wait()
	if secondErr != nil || secondResult.Text != "hello" {
		t.Fatalf("second Wait() = %#v, %v", secondResult, secondErr)
	}
	secondResult.Response.Content = "changed"
	thirdResult, _ := stream.Wait()
	if thirdResult.Response.Content != "hello" {
		t.Fatalf("mutating Wait() result changed stored result to %q", thirdResult.Response.Content)
	}
}

func TestStreamWaitDoesNotDependOnEventConsumption(t *testing.T) {
	ctx := context.Background()
	chunks := make([]string, 500)
	for i := range chunks {
		chunks[i] = "x"
	}
	agent, err := New(ctx, &Config{Model: NewMockChatModel(MockModelStream(chunks...))})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	stream, err := agent.Stream(ctx, "produce output")
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	select {
	case <-stream.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("stream execution blocked because Events was not consumed")
	}
	result, err := stream.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if len(result.Text) != len(chunks) {
		t.Fatalf("result text length = %d, want %d", len(result.Text), len(chunks))
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestRunStreamCloseCancelsOnlyItsRun(t *testing.T) {
	ctx := context.Background()
	toolStarted := make(chan struct{})
	waitTool := MustMockTool("wait", "wait for cancellation", func(ctx context.Context, _ *resultEchoInput) (string, error) {
		close(toolStarted)
		<-ctx.Done()
		return "", ctx.Err()
	})
	call, err := waitTool.Invocation("wait-call", &resultEchoInput{Text: "wait"})
	if err != nil {
		t.Fatalf("Invocation() error = %v", err)
	}
	agent, err := New(ctx, &Config{
		Model: NewMockChatModel(MockModelCalls(call), MockModelText("next run")),
		Tools: MockTools(waitTool),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	stream, err := agent.Stream(ctx, "wait")
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	select {
	case <-toolStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	partial, err := stream.Wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
	if partial == nil || len(partial.Messages) == 0 {
		t.Fatalf("partial result = %#v", partial)
	}

	agent.Reset()
	result, err := agent.Ask(ctx, "run again")
	if err != nil {
		t.Fatalf("Ask() after canceled stream error = %v", err)
	}
	if result.Text != "next run" {
		t.Fatalf("Ask() after canceled stream text = %q", result.Text)
	}
}

func TestStreamPartsClonesInputBeforeReturning(t *testing.T) {
	ctx := context.Background()
	part := ImageURL("https://example.com/original.png")
	agent, err := New(ctx, &Config{
		Model: NewMockChatModel(MockExpect(MockModelText("seen"), func(call MockModelCall) error {
			got := call.Input[len(call.Input)-1].UserInputMultiContent[0].Image.URL
			if got == nil || *got != "https://example.com/original.png" {
				return errors.New("stream input was mutated by caller")
			}
			return nil
		})),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()

	stream, err := agent.StreamParts(ctx, part)
	if err != nil {
		t.Fatalf("StreamParts() error = %v", err)
	}
	changed := "https://example.com/changed.png"
	part.Image.URL = &changed
	for range stream.Events() {
	}
	result, err := stream.Wait()
	if err != nil || result.Text != "seen" {
		t.Fatalf("Wait() = %#v, %v", result, err)
	}
}
