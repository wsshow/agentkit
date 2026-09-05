package agentkit

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cloudwego/eino/schema"
)

// ErrModelPanic 表示第三方模型实现在生成或读取流时发生 panic。
var ErrModelPanic = errors.New("agentkit: model panicked")

type guardedChatModel struct {
	model ChatModel
}

func guardChatModel(model ChatModel) ChatModel {
	if model == nil {
		return nil
	}
	if _, guarded := model.(*guardedChatModel); guarded {
		return model
	}
	return &guardedChatModel{model: model}
}

func (m *guardedChatModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	options ...ModelOption,
) (output *schema.Message, err error) {
	defer recoverModelPanic("Generate", &err)
	return m.model.Generate(ctx, input, options...)
}

func (m *guardedChatModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	options ...ModelOption,
) (output *schema.StreamReader[*schema.Message], err error) {
	defer recoverModelPanic("Stream", &err)
	output, err = m.model.Stream(ctx, input, options...)
	if err != nil || output == nil {
		return output, err
	}
	return guardModelStream(output), nil
}

func recoverModelPanic(operation string, err *error) {
	if value := recover(); value != nil {
		*err = fmt.Errorf("%w in %s: %v", ErrModelPanic, operation, value)
	}
}

func guardModelStream(source *schema.StreamReader[*schema.Message]) *schema.StreamReader[*schema.Message] {
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		defer func() {
			if value := recover(); value != nil {
				writer.Send(nil, fmt.Errorf("%w in Stream.Recv: %v", ErrModelPanic, value))
			}
		}()
		defer source.Close()
		for {
			message, err := source.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if writer.Send(message, err) || err != nil {
				return
			}
		}
	}()
	return reader
}
