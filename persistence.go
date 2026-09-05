package agentkit

import (
	"context"
	"time"
)

// DefaultPersistenceTimeout 是取消后的内部持久化收尾 context 默认时限。
// 自定义存储必须及时响应 context，时限才能按预期生效。
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
