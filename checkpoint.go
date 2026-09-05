package agentkit

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/cloudwego/eino/compose"
)

// CheckpointStore 保存可恢复执行所需的检查点。
// 实现必须及时响应每个方法的 context 取消与截止时间；Set 不得保留调用方传入的可变字节切片。
type CheckpointStore = compose.CheckPointStore

// CheckpointDeleter 是支持显式删除检查点的可选接口。
// 内置存储均实现该接口；自定义存储也应实现它，以便 Reset、SetHistory
// 和成功恢复后能够及时清理失效检查点。Delete 必须及时响应 context 取消与截止时间。
type CheckpointDeleter interface {
	Delete(ctx context.Context, id string) error
}

// CheckpointStoreProvider 可由 SessionStore 选择性实现，为会话提供配套的检查点存储。
// Config.CheckPointStore 未设置时，Agent 会优先使用该存储。
type CheckpointStoreProvider interface {
	CheckpointStore() CheckpointStore
}

// MemoryCheckpointStore 是并发安全的内存检查点存储，适合测试和单进程服务。
type MemoryCheckpointStore struct {
	mu          sync.RWMutex
	checkpoints map[string][]byte
}

var (
	_ CheckpointStore   = (*MemoryCheckpointStore)(nil)
	_ CheckpointDeleter = (*MemoryCheckpointStore)(nil)
)

// NewMemoryCheckpointStore 创建内存检查点存储。
func NewMemoryCheckpointStore() *MemoryCheckpointStore {
	return &MemoryCheckpointStore{checkpoints: make(map[string][]byte)}
}

// Set 保存并完全替换检查点。
func (s *MemoryCheckpointStore) Set(ctx context.Context, id string, value []byte) error {
	if err := validateCheckpointContextAndID(ctx, id); err != nil {
		return err
	}
	cloned := append([]byte(nil), value...)
	s.mu.Lock()
	if s.checkpoints == nil {
		s.checkpoints = make(map[string][]byte)
	}
	s.checkpoints[id] = cloned
	s.mu.Unlock()
	return nil
}

// Get 加载检查点。检查点不存在时 existed 为 false。
func (s *MemoryCheckpointStore) Get(ctx context.Context, id string) (value []byte, existed bool, err error) {
	if err := validateCheckpointContextAndID(ctx, id); err != nil {
		return nil, false, err
	}
	s.mu.RLock()
	value, existed = s.checkpoints[id]
	s.mu.RUnlock()
	return append([]byte(nil), value...), existed, nil
}

// Delete 删除检查点。检查点不存在时也返回 nil。
func (s *MemoryCheckpointStore) Delete(ctx context.Context, id string) error {
	if err := validateCheckpointContextAndID(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.checkpoints, id)
	s.mu.Unlock()
	return nil
}

func validateCheckpointContextAndID(ctx context.Context, id string) error {
	if ctx == nil {
		return errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("agentkit: checkpoint ID is required")
	}
	return nil
}
