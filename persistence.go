package agentkit

import (
	"context"
	"time"
)

// DefaultPersistenceTimeout 限制取消后的内部持久化收尾，避免异常存储永久阻塞任务退出。
const DefaultPersistenceTimeout = 30 * time.Second

func newPersistenceContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.TODO()
	}
	if timeout <= 0 {
		timeout = DefaultPersistenceTimeout
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}

func (a *Agent) persistenceContext(parent context.Context) (context.Context, context.CancelFunc) {
	return newPersistenceContext(parent, a.persistenceTimeout)
}

func (r *GoalRunner) persistenceContext(parent context.Context) (context.Context, context.CancelFunc) {
	return newPersistenceContext(parent, r.agent.persistenceTimeout)
}
