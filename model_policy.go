package agentkit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// ErrModelPolicyPanic 表示模型重试或故障切换的用户回调发生 panic。
var ErrModelPolicyPanic = errors.New("agentkit: model policy callback panicked")

func guardedModelRetryConfig(agent *Agent, config *ModelRetryConfig) *ModelRetryConfig {
	if config == nil {
		return nil
	}
	guarded := *config
	if config.ShouldRetry != nil {
		shouldRetry := config.ShouldRetry
		guarded.ShouldRetry = func(ctx context.Context, retry *adk.RetryContext) (decision *adk.RetryDecision) {
			defer func() {
				if value := recover(); value != nil {
					decision = &adk.RetryDecision{RewriteError: modelPolicyPanicError("ShouldRetry", value)}
				}
			}()
			return shouldRetry(ctx, retry)
		}
	} else {
		//lint:ignore SA1019 ModelRetryConfig retains IsRetryAble for Eino compatibility.
		isRetryAble := config.IsRetryAble
		if isRetryAble != nil {
			//lint:ignore SA1019 ModelRetryConfig retains IsRetryAble for Eino compatibility.
			guarded.IsRetryAble = func(ctx context.Context, err error) (retry bool) {
				defer func() {
					if value := recover(); value != nil {
						agent.emitModelPolicyPanic("IsRetryAble", value)
						retry = false
					}
				}()
				return isRetryAble(ctx, err)
			}
		}
	}
	if config.BackoffFunc != nil {
		backoff := config.BackoffFunc
		guarded.BackoffFunc = func(ctx context.Context, attempt int) (delay time.Duration) {
			defer func() {
				if value := recover(); value != nil {
					agent.emitModelPolicyPanic("BackoffFunc", value)
					delay = 0
				}
			}()
			return backoff(ctx, attempt)
		}
	}
	return &guarded
}

func guardedModelFailoverConfig(agent *Agent, config *ModelFailoverConfig) *ModelFailoverConfig {
	if config == nil {
		return nil
	}
	guarded := *config
	if config.ShouldFailover != nil {
		shouldFailover := config.ShouldFailover
		guarded.ShouldFailover = func(ctx context.Context, output *schema.Message, err error) (failover bool) {
			defer func() {
				if value := recover(); value != nil {
					agent.emitModelPolicyPanic("ShouldFailover", value)
					failover = false
				}
			}()
			return shouldFailover(ctx, output, err)
		}
	}
	if config.GetFailoverModel != nil {
		getFailoverModel := config.GetFailoverModel
		guarded.GetFailoverModel = func(
			ctx context.Context,
			failover *adk.FailoverContext[*schema.Message],
		) (chatModel ChatModel, input []*schema.Message, err error) {
			defer func() {
				if value := recover(); value != nil {
					chatModel = nil
					input = nil
					err = modelPolicyPanicError("GetFailoverModel", value)
				}
			}()
			chatModel, input, err = getFailoverModel(ctx, failover)
			return guardChatModel(chatModel), input, err
		}
	}
	return &guarded
}

func (a *Agent) emitModelPolicyPanic(callback string, value any) {
	if a == nil || a.emtr == nil {
		return
	}
	a.emtr.Emit(Event{
		Type: EventError, Agent: a.name, Error: modelPolicyPanicError(callback, value),
	})
}

func modelPolicyPanicError(callback string, value any) error {
	return fmt.Errorf("%w in %s: %v", ErrModelPolicyPanic, callback, value)
}
