package agentkit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrToolResultNotFound 表示存储中不存在指定的大型工具结果。
	ErrToolResultNotFound = errors.New("agentkit: tool result not found")
	// ErrToolResultExists 表示相同 ID 的不可变工具结果已经存在。
	ErrToolResultExists = errors.New("agentkit: tool result already exists")
)

// StoredToolResult 是从模型上下文卸载的完整文本工具结果。
type StoredToolResult struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// ToolResultInfo 是用于结果清理和监控的轻量元数据。
type ToolResultInfo struct {
	ID        string    `json:"id"`
	Size      int       `json:"size"` // Content 的字节数
	CreatedAt time.Time `json:"created_at"`
}

// ToolResultStore 管理从上下文卸载的不可变工具结果。
// Load 在结果不存在时必须返回包装 ErrToolResultNotFound 的错误。
// Save 只允许创建新 ID，重复 ID 必须返回包装 ErrToolResultExists 的错误。
// Delete 必须幂等；所有方法必须可以安全地被多个 goroutine 调用。
type ToolResultStore interface {
	Load(ctx context.Context, id string) (*StoredToolResult, error)
	Save(ctx context.Context, result *StoredToolResult) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]ToolResultInfo, error)
}

// ToolResultStoreProvider 允许会话存储提供配套的大型工具结果存储。
type ToolResultStoreProvider interface {
	ToolResultStore() ToolResultStore
}

// MemoryToolResultStore 是并发安全的内存结果存储。
type MemoryToolResultStore struct {
	mu      sync.RWMutex
	results map[string]*StoredToolResult
}

var _ ToolResultStore = (*MemoryToolResultStore)(nil)

// NewMemoryToolResultStore 创建内存结果存储。
func NewMemoryToolResultStore() *MemoryToolResultStore {
	return &MemoryToolResultStore{results: make(map[string]*StoredToolResult)}
}

// Load 加载完整工具结果。
func (s *MemoryToolResultStore) Load(ctx context.Context, id string) (*StoredToolResult, error) {
	if err := validateToolResultContextAndID(ctx, id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	result, ok := s.results[id]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrToolResultNotFound, id)
	}
	return cloneStoredToolResult(result), nil
}

// Save 创建一个不可变工具结果。
func (s *MemoryToolResultStore) Save(ctx context.Context, result *StoredToolResult) error {
	if err := validateStoredToolResult(ctx, result); err != nil {
		return err
	}
	cloned := normalizedStoredToolResult(result)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.results == nil {
		s.results = make(map[string]*StoredToolResult)
	}
	if _, exists := s.results[result.ID]; exists {
		return fmt.Errorf("%w: %s", ErrToolResultExists, result.ID)
	}
	s.results[result.ID] = cloned
	return nil
}

// Delete 删除结果；结果不存在时也返回 nil。
func (s *MemoryToolResultStore) Delete(ctx context.Context, id string) error {
	if err := validateToolResultContextAndID(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.results, id)
	s.mu.Unlock()
	return nil
}

// List 按创建时间从新到旧列出结果。
func (s *MemoryToolResultStore) List(ctx context.Context) ([]ToolResultInfo, error) {
	if err := validateToolResultContext(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	infos := make([]ToolResultInfo, 0, len(s.results))
	for _, result := range s.results {
		infos = append(infos, storedToolResultInfo(result))
	}
	s.mu.RUnlock()
	sortToolResultInfos(infos)
	return infos, nil
}

func validateToolResultContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("agentkit: context is required")
	}
	return ctx.Err()
}

func validateToolResultContextAndID(ctx context.Context, id string) error {
	if err := validateToolResultContext(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("agentkit: tool result ID is required")
	}
	return nil
}

func validateStoredToolResult(ctx context.Context, result *StoredToolResult) error {
	if result == nil {
		return errors.New("agentkit: tool result is required")
	}
	return validateToolResultContextAndID(ctx, result.ID)
}

func normalizedStoredToolResult(result *StoredToolResult) *StoredToolResult {
	cloned := cloneStoredToolResult(result)
	if cloned.CreatedAt.IsZero() {
		cloned.CreatedAt = time.Now().UTC()
	}
	return cloned
}

func cloneStoredToolResult(result *StoredToolResult) *StoredToolResult {
	if result == nil {
		return nil
	}
	cloned := *result
	return &cloned
}

func storedToolResultInfo(result *StoredToolResult) ToolResultInfo {
	return ToolResultInfo{
		ID: result.ID, Size: len(result.Content), CreatedAt: result.CreatedAt,
	}
}

func sortToolResultInfos(infos []ToolResultInfo) {
	sort.SliceStable(infos, func(i, j int) bool {
		if infos[i].CreatedAt.Equal(infos[j].CreatedAt) {
			return infos[i].ID < infos[j].ID
		}
		return infos[i].CreatedAt.After(infos[j].CreatedAt)
	})
}
