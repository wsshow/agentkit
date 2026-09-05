package agentkit

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func generateModelWithFailover(
	ctx context.Context,
	primary ChatModel,
	input []*schema.Message,
	retry *ModelRetryConfig,
	failover *ModelFailoverConfig,
) (*schema.Message, error) {
	output, err := generateModelWithRetry(ctx, primary, input, retry)
	if err == nil || failover == nil || failover.GetFailoverModel == nil || failover.ShouldFailover == nil {
		return output, err
	}
	if shouldStopModelRetry(ctx, err) || !failover.ShouldFailover(ctx, output, err) {
		return output, err
	}

	lastOutput, lastErr := output, err
	for attempt := uint(1); attempt <= failover.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current, currentInput, err := failover.GetFailoverModel(ctx, &adk.FailoverContext[*schema.Message]{
			FailoverAttempt:   attempt,
			InputMessages:     input,
			LastOutputMessage: lastOutput,
			LastErr:           lastErr,
		})
		if err != nil {
			return nil, err
		}
		if current == nil {
			return nil, fmt.Errorf("agentkit: failover model is nil at attempt %d", attempt)
		}
		if currentInput == nil {
			currentInput = input
		}
		lastOutput, lastErr = generateModelWithRetry(ctx, current, currentInput, retry)
		if lastErr == nil {
			return lastOutput, nil
		}
		if shouldStopModelRetry(ctx, lastErr) || !failover.ShouldFailover(ctx, lastOutput, lastErr) {
			return lastOutput, lastErr
		}
	}
	return lastOutput, lastErr
}
