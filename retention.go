package agentkit

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RetentionPolicy 配置一次显式资源清扫。零值不删除任何数据。
type RetentionPolicy struct {
	// SessionIdleTime 删除超过该时长未更新的会话；删除内置会话时会级联清理关联资源。
	SessionIdleTime time.Duration
	// CompletedGoalAge 删除超过该时长未更新的已完成目标；不会删除 active、paused 或 blocked 目标。
	CompletedGoalAge time.Duration
	// DetachedToolResultAge 删除超过该时长且 SessionID 为空的工具结果。
	DetachedToolResultAge time.Duration
}

// RetentionReport 汇总一次资源清扫直接删除的条目数。
// SessionStore.Delete 内部级联删除的资源不重复计入其他字段。
type RetentionReport struct {
	SessionsDeleted            int
	CompletedGoalsDeleted      int
	DetachedToolResultsDeleted int
}

// PruneResources 按保留策略显式清扫资源。
// 它不会启动后台任务；调用方应确保待删除会话已没有运行中的 worker。
func PruneResources(
	ctx context.Context,
	sessions SessionStore,
	policy RetentionPolicy,
) (RetentionReport, error) {
	return pruneResourcesAt(ctx, sessions, policy, time.Now().UTC())
}

func pruneResourcesAt(
	ctx context.Context,
	sessions SessionStore,
	policy RetentionPolicy,
	now time.Time,
) (RetentionReport, error) {
	var report RetentionReport
	if ctx == nil {
		return report, errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if sessions == nil {
		return report, errors.New("agentkit: session store is required")
	}
	if err := validateRetentionPolicy(policy); err != nil {
		return report, err
	}

	var goals GoalStore
	if policy.CompletedGoalAge > 0 {
		provider, ok := sessions.(GoalStoreProvider)
		if !ok {
			return report, errors.New("agentkit: completed goal retention requires GoalStoreProvider")
		}
		goals = provider.GoalStore()
		if goals == nil {
			return report, errors.New("agentkit: goal store provider returned nil")
		}
	}
	var toolResults ToolResultStore
	if policy.DetachedToolResultAge > 0 {
		provider, ok := sessions.(ToolResultStoreProvider)
		if !ok {
			return report, errors.New("agentkit: detached tool result retention requires ToolResultStoreProvider")
		}
		toolResults = provider.ToolResultStore()
		if toolResults == nil {
			return report, errors.New("agentkit: tool result store provider returned nil")
		}
	}

	var errs []error
	if policy.SessionIdleTime > 0 {
		infos, err := sessions.List(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("agentkit: list sessions for retention: %w", err))
		} else {
			cutoff := now.Add(-policy.SessionIdleTime)
			for _, info := range infos {
				if retentionContextDone(ctx, &errs) {
					break
				}
				if info.UpdatedAt.IsZero() || info.UpdatedAt.After(cutoff) {
					continue
				}
				if err := sessions.Delete(ctx, info.ID); err != nil {
					errs = append(errs, fmt.Errorf("agentkit: prune session %q: %w", info.ID, err))
					continue
				}
				report.SessionsDeleted++
			}
		}
	}
	if policy.CompletedGoalAge > 0 && !retentionContextDone(ctx, &errs) {
		infos, err := goals.List(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("agentkit: list completed goals for retention: %w", err))
		} else {
			cutoff := now.Add(-policy.CompletedGoalAge)
			for _, info := range infos {
				if retentionContextDone(ctx, &errs) {
					break
				}
				if info.Status != GoalStatusCompleted || info.UpdatedAt.IsZero() || info.UpdatedAt.After(cutoff) {
					continue
				}
				if err := goals.Delete(ctx, info.ID); err != nil {
					errs = append(errs, fmt.Errorf("agentkit: prune completed goal %q: %w", info.ID, err))
					continue
				}
				report.CompletedGoalsDeleted++
			}
		}
	}
	if policy.DetachedToolResultAge > 0 && !retentionContextDone(ctx, &errs) {
		infos, err := toolResults.List(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("agentkit: list detached tool results for retention: %w", err))
		} else {
			cutoff := now.Add(-policy.DetachedToolResultAge)
			for _, info := range infos {
				if retentionContextDone(ctx, &errs) {
					break
				}
				if info.SessionID != "" || info.CreatedAt.IsZero() || info.CreatedAt.After(cutoff) {
					continue
				}
				if err := toolResults.Delete(ctx, info.ID); err != nil {
					errs = append(errs, fmt.Errorf("agentkit: prune detached tool result %q: %w", info.ID, err))
					continue
				}
				report.DetachedToolResultsDeleted++
			}
		}
	}
	return report, errors.Join(errs...)
}

func validateRetentionPolicy(policy RetentionPolicy) error {
	if policy.SessionIdleTime < 0 {
		return fmt.Errorf("agentkit: session idle retention must not be negative: %s", policy.SessionIdleTime)
	}
	if policy.CompletedGoalAge < 0 {
		return fmt.Errorf("agentkit: completed goal retention must not be negative: %s", policy.CompletedGoalAge)
	}
	if policy.DetachedToolResultAge < 0 {
		return fmt.Errorf("agentkit: detached tool result retention must not be negative: %s", policy.DetachedToolResultAge)
	}
	return nil
}

func retentionContextDone(ctx context.Context, errs *[]error) bool {
	if err := ctx.Err(); err != nil {
		if !errors.Is(errors.Join(*errs...), err) {
			*errs = append(*errs, err)
		}
		return true
	}
	return false
}
