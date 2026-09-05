package agentkit

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func generateModelWithRetry(
	ctx context.Context,
	chatModel ChatModel,
	input []*schema.Message,
	config *ModelRetryConfig,
	options ...ModelOption,
) (*schema.Message, error) {
	if config == nil {
		return chatModel.Generate(ctx, input, options...)
	}
	maxRetries := config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if config.ShouldRetry == nil {
		return generateModelWithLegacyRetry(ctx, chatModel, input, config, maxRetries, options...)
	}

	currentInput := input
	currentOptions := append([]ModelOption(nil), options...)
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		output, err := chatModel.Generate(ctx, currentInput, currentOptions...)
		if shouldStopModelRetry(ctx, err) {
			return nil, err
		}
		decision := config.ShouldRetry(ctx, &adk.RetryContext{
			RetryAttempt:  attempt + 1,
			InputMessages: currentInput,
			Options:       currentOptions,
			OutputMessage: output,
			Err:           err,
		})
		if decision == nil {
			decision = &adk.RetryDecision{}
		}
		if !decision.Retry {
			if decision.RewriteError != nil {
				return nil, decision.RewriteError
			}
			return output, err
		}

		lastErr = err
		if lastErr == nil {
			lastErr = fmt.Errorf("model output rejected by ShouldRetry at attempt %d", attempt+1)
		}
		if attempt >= maxRetries {
			break
		}
		if decision.ModifiedInputMessages != nil {
			currentInput = decision.ModifiedInputMessages
		}
		currentOptions = append(currentOptions, decision.AdditionalOptions...)
		delay := decision.Backoff
		if delay == 0 {
			delay = modelRetryBackoff(config, ctx, attempt+1)
		}
		if err := waitForModelRetry(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, &adk.RetryExhaustedError{LastErr: lastErr, TotalRetries: maxRetries}
}

func generateModelWithLegacyRetry(
	ctx context.Context,
	chatModel ChatModel,
	input []*schema.Message,
	config *ModelRetryConfig,
	maxRetries int,
	options ...ModelOption,
) (*schema.Message, error) {
	//lint:ignore SA1019 ModelRetryConfig still supports IsRetryAble for Eino compatibility.
	shouldRetry := config.IsRetryAble
	if shouldRetry == nil {
		shouldRetry = func(_ context.Context, err error) bool { return err != nil }
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		output, err := chatModel.Generate(ctx, input, options...)
		if err == nil {
			return output, nil
		}
		if shouldStopModelRetry(ctx, err) || !shouldRetry(ctx, err) {
			return nil, err
		}
		lastErr = err
		if attempt < maxRetries {
			if err := waitForModelRetry(ctx, modelRetryBackoff(config, ctx, attempt+1)); err != nil {
				return nil, err
			}
		}
	}
	return nil, &adk.RetryExhaustedError{LastErr: lastErr, TotalRetries: maxRetries}
}

func shouldStopModelRetry(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil || errors.Is(err, adk.ErrStreamCanceled) {
		return true
	}
	_, interrupted := compose.ExtractInterruptInfo(err)
	return interrupted
}

func modelRetryBackoff(config *ModelRetryConfig, ctx context.Context, attempt int) time.Duration {
	if config.BackoffFunc != nil {
		return config.BackoffFunc(ctx, attempt)
	}
	base := 100 * time.Millisecond
	maximum := 10 * time.Second
	if attempt > 7 {
		base = maximum
	} else if attempt > 0 {
		base *= time.Duration(1 << uint(attempt-1))
	}
	if base > maximum {
		base = maximum
	}
	return base + time.Duration(rand.Int63n(int64(base/2)))
}

func waitForModelRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
