package agentkit

import (
	"context"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// DefaultToolResultMaxChars 是工具文本结果的默认字符上限。
const DefaultToolResultMaxChars = 100_000

const toolResultTruncatedMarker = "\n...[tool result truncated]"

// ToolInvocation 描述一次即将执行的工具调用。
type ToolInvocation struct {
	// Name 是解析别名后的正式工具名。
	Name string
	// Arguments 是传给工具的 JSON 参数。
	Arguments string
	// CallID 是模型生成的工具调用 ID。
	CallID string
}

// ToolOutcome 描述一次工具调用的执行结果，适合用于审计和指标记录。
type ToolOutcome struct {
	// Err 是工具或保护策略返回的错误。
	Err error
	// Truncated 表示文本结果是否因超过上限而被截断。
	Truncated bool
	// OutputChars 是最终保留的原始文本字符数，不包含截断提示。
	OutputChars int
	// Duration 是从前置钩子开始到工具结果处理完成的耗时。
	Duration time.Duration
}

func (p *ToolPolicy) executionMiddleware() compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable:          p.guardInvokable,
		Streamable:         p.guardStreamable,
		EnhancedInvokable:  p.guardEnhancedInvokable,
		EnhancedStreamable: p.guardEnhancedStreamable,
	}
}

func (p *ToolPolicy) maxResultChars() int {
	if p == nil || p.MaxResultChars == 0 {
		return DefaultToolResultMaxChars
	}
	return p.MaxResultChars
}

func (p *ToolPolicy) startTool(ctx context.Context, input *compose.ToolInput) (context.Context, context.CancelFunc, ToolInvocation, time.Time, error) {
	call := invocationFromInput(input)
	started := time.Now()
	runCtx, cancel := ctx, func() {}
	if p != nil && p.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, p.Timeout)
	}
	if p != nil && p.BeforeTool != nil {
		if err := p.BeforeTool(runCtx, call); err != nil {
			cancel()
			p.finishTool(ctx, call, started, ToolOutcome{Err: err})
			return runCtx, func() {}, call, started, err
		}
	}
	return runCtx, cancel, call, started, nil
}

func (p *ToolPolicy) finishTool(ctx context.Context, call ToolInvocation, started time.Time, outcome ToolOutcome) {
	outcome.Duration = time.Since(started)
	if p != nil && p.AfterTool != nil {
		p.AfterTool(ctx, call, outcome)
	}
}

func invocationFromInput(input *compose.ToolInput) ToolInvocation {
	if input == nil {
		return ToolInvocation{}
	}
	return ToolInvocation{Name: input.Name, Arguments: input.Arguments, CallID: input.CallID}
}

func (p *ToolPolicy) guardInvokable(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		runCtx, cancel, call, started, err := p.startTool(ctx, input)
		if err != nil {
			return nil, err
		}
		defer cancel()

		output, err := next(runCtx, input)
		if err == nil && runCtx.Err() != nil {
			err = runCtx.Err()
			output = nil
		}
		outcome := ToolOutcome{Err: err}
		if err == nil && output != nil {
			output = &compose.ToolOutput{Result: output.Result}
			output.Result, outcome.OutputChars, outcome.Truncated = limitText(output.Result, p.maxResultChars())
		}
		p.finishTool(ctx, call, started, outcome)
		return output, err
	}
}

func (p *ToolPolicy) guardStreamable(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		runCtx, cancel, call, started, err := p.startTool(ctx, input)
		if err != nil {
			return nil, err
		}
		output, err := next(runCtx, input)
		if err == nil && runCtx.Err() != nil {
			err = runCtx.Err()
		}
		if err != nil || output == nil || output.Result == nil {
			cancel()
			p.finishTool(ctx, call, started, ToolOutcome{Err: err})
			return output, err
		}

		reader := guardTextStream(runCtx, output.Result, p.maxResultChars(), func(outcome ToolOutcome) {
			cancel()
			p.finishTool(ctx, call, started, outcome)
		})
		return &compose.StreamToolOutput{Result: reader}, nil
	}
}

func (p *ToolPolicy) guardEnhancedInvokable(next compose.EnhancedInvokableToolEndpoint) compose.EnhancedInvokableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
		runCtx, cancel, call, started, err := p.startTool(ctx, input)
		if err != nil {
			return nil, err
		}
		defer cancel()

		output, err := next(runCtx, input)
		if err == nil && runCtx.Err() != nil {
			err = runCtx.Err()
			output = nil
		}
		outcome := ToolOutcome{Err: err}
		if err == nil && output != nil && output.Result != nil {
			result, chars, truncated := limitToolResult(output.Result, p.maxResultChars())
			output = &compose.EnhancedInvokableToolOutput{Result: result}
			outcome.OutputChars, outcome.Truncated = chars, truncated
		}
		p.finishTool(ctx, call, started, outcome)
		return output, err
	}
}

func (p *ToolPolicy) guardEnhancedStreamable(next compose.EnhancedStreamableToolEndpoint) compose.EnhancedStreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
		runCtx, cancel, call, started, err := p.startTool(ctx, input)
		if err != nil {
			return nil, err
		}
		output, err := next(runCtx, input)
		if err == nil && runCtx.Err() != nil {
			err = runCtx.Err()
		}
		if err != nil || output == nil || output.Result == nil {
			cancel()
			p.finishTool(ctx, call, started, ToolOutcome{Err: err})
			return output, err
		}

		reader := guardToolResultStream(runCtx, output.Result, p.maxResultChars(), func(outcome ToolOutcome) {
			cancel()
			p.finishTool(ctx, call, started, outcome)
		})
		return &compose.EnhancedStreamableToolOutput{Result: reader}, nil
	}
}

func limitText(value string, maxChars int) (string, int, bool) {
	if maxChars < 0 {
		return value, utf8.RuneCountInString(value), false
	}
	if len(value) <= maxChars {
		return value, len(value), false
	}
	runes := []rune(value)
	if len(runes) <= maxChars {
		return value, len(runes), false
	}
	return string(runes[:maxChars]) + toolResultTruncatedMarker, maxChars, true
}

func limitToolResult(result *schema.ToolResult, maxChars int) (*schema.ToolResult, int, bool) {
	if result == nil {
		return nil, 0, false
	}
	limited := &schema.ToolResult{Parts: make([]schema.ToolOutputPart, 0, len(result.Parts))}
	remaining, chars, truncated := maxChars, 0, false
	for _, part := range result.Parts {
		if part.Type != schema.ToolPartTypeText || maxChars < 0 {
			limited.Parts = append(limited.Parts, part)
			if part.Type == schema.ToolPartTypeText {
				chars += utf8.RuneCountInString(part.Text)
			}
			continue
		}
		if truncated {
			continue
		}
		text, kept, cut := limitText(part.Text, remaining)
		part.Text = text
		limited.Parts = append(limited.Parts, part)
		chars += kept
		remaining -= kept
		truncated = cut
	}
	return limited, chars, truncated
}

func guardTextStream(ctx context.Context, source *schema.StreamReader[string], maxChars int, finish func(ToolOutcome)) *schema.StreamReader[string] {
	reader, writer := schema.Pipe[string](1)
	go func() {
		defer source.Close()
		defer writer.Close()
		outcome := ToolOutcome{}
		defer func() { finish(outcome) }()
		remaining := maxChars
		for {
			chunk, err := source.Recv()
			if errors.Is(err, io.EOF) {
				if ctx.Err() != nil {
					outcome.Err = ctx.Err()
					writer.Send("", outcome.Err)
				}
				return
			}
			if err != nil {
				outcome.Err = err
				writer.Send("", err)
				return
			}
			limited, kept, truncated := limitText(chunk, remaining)
			outcome.OutputChars += kept
			if remaining >= 0 {
				remaining -= kept
			}
			outcome.Truncated = truncated
			if limited != "" && writer.Send(limited, nil) {
				return
			}
			if truncated {
				return
			}
		}
	}()
	return reader
}

func guardToolResultStream(ctx context.Context, source *schema.StreamReader[*schema.ToolResult], maxChars int, finish func(ToolOutcome)) *schema.StreamReader[*schema.ToolResult] {
	reader, writer := schema.Pipe[*schema.ToolResult](1)
	go func() {
		defer source.Close()
		defer writer.Close()
		outcome := ToolOutcome{}
		defer func() { finish(outcome) }()
		remaining := maxChars
		for {
			chunk, err := source.Recv()
			if errors.Is(err, io.EOF) {
				if ctx.Err() != nil {
					outcome.Err = ctx.Err()
					writer.Send(nil, outcome.Err)
				}
				return
			}
			if err != nil {
				outcome.Err = err
				writer.Send(nil, err)
				return
			}
			limited, kept, truncated := limitToolResult(chunk, remaining)
			outcome.OutputChars += kept
			if remaining >= 0 {
				remaining -= kept
			}
			outcome.Truncated = truncated
			if writer.Send(limited, nil) {
				return
			}
			if truncated {
				return
			}
		}
	}()
	return reader
}
