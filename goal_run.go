package agentkit

import (
	"context"
	"errors"
	"sync"
)

// GoalRun 表示一次正在后台推进的持久化目标。
// 目标在 StartAsync 返回前已经创建并落盘，可立即通过 ID、Get 或 List 对外提供状态。
type GoalRun struct {
	runner *GoalRunner
	id     string
	done   chan struct{}

	mu     sync.Mutex
	result *GoalRunResult
	err    error
}

// ID 返回持久化目标 ID。
func (r *GoalRun) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

// Done 在本次后台推进停止时关闭。
func (r *GoalRun) Done() <-chan struct{} {
	if r == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return r.done
}

// Wait 等待本次后台推进停止并返回隔离的结果副本。
func (r *GoalRun) Wait() (*GoalRunResult, error) {
	return r.WaitContext(context.Background())
}

// WaitContext 等待本次后台推进停止或 ctx 结束；等待超时不会取消目标。
// 可在超时后再次调用 Wait 或 WaitContext。
func (r *GoalRun) WaitContext(ctx context.Context) (*GoalRunResult, error) {
	if r == nil {
		return nil, nil
	}
	if ctx == nil {
		return nil, errors.New("agentkit: context is required")
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneGoalRunResult(r.result), r.err
}

// Pause 持久化暂停目标并取消本次后台推进。
func (r *GoalRun) Pause(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.runner.Pause(ctx, r.id)
}

func (r *GoalRun) complete(result *GoalRunResult, err error) {
	r.mu.Lock()
	r.result = cloneGoalRunResult(result)
	r.err = err
	r.mu.Unlock()
	close(r.done)
}

func cloneGoalRunResult(result *GoalRunResult) *GoalRunResult {
	if result == nil {
		return nil
	}
	return &GoalRunResult{
		Goal:    cloneGoal(result.Goal),
		LastRun: cloneRunResult(result.LastRun),
	}
}
