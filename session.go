package agentkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

var (
	// ErrSessionNotFound 表示会话存储中不存在指定会话。
	ErrSessionNotFound = errors.New("agentkit: session not found")
	// ErrSessionConflict 表示会话已被其他调用方更新，当前快照不能覆盖新状态。
	ErrSessionConflict = errors.New("agentkit: session revision conflict")
	// ErrSessionDisabled 表示 Agent 未配置会话存储。
	ErrSessionDisabled = errors.New("agentkit: session persistence is not configured")
	// ErrSessionArchived 表示会话已经归档，不能再创建运行实例。
	ErrSessionArchived = errors.New("agentkit: session is archived")
)

// SessionConfig 配置 Agent 的自动会话恢复与持久化。
type SessionConfig struct {
	ID    string       // 在存储中唯一且稳定的会话标识
	Store SessionStore // 会话存储
}

// SessionMetadata 是用于组织和检索会话的应用级元数据。
// OwnerID 可表示用户、租户或应用自己的隔离命名空间。
type SessionMetadata struct {
	Title   string   `json:"title,omitempty"`
	OwnerID string   `json:"owner_id,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

// Session 是可持久化的完整对话快照。
type Session struct {
	SessionMetadata
	ID                string            `json:"id"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	Messages          []*schema.Message `json:"messages"`                     // 未删减的完整对话
	Context           []*schema.Message `json:"context,omitempty"`            // 压缩后的模型上下文；nil 表示与 Messages 相同
	CheckpointID      string            `json:"checkpoint_id,omitempty"`      // 当前可恢复执行的检查点标识
	PendingInterrupts []InterruptPoint  `json:"pending_interrupts,omitempty"` // 等待 Resume 的中断点
	Archived          bool              `json:"archived,omitempty"`           // 归档后不可再运行，但仍可查询和恢复
	Revision          uint64            `json:"revision"`                     // 乐观并发控制版本
}

// SessionInfo 是用于会话列表展示的轻量元数据。
type SessionInfo struct {
	SessionMetadata
	ID                    string    `json:"id"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
	MessageCount          int       `json:"message_count"`
	ContextMessageCount   int       `json:"context_message_count"`
	PendingInterruptCount int       `json:"pending_interrupt_count"`
	Archived              bool      `json:"archived,omitempty"`
	Revision              uint64    `json:"revision"`
}

// SessionStore 管理多个持久化会话。
// Load 在会话不存在时必须返回包装 ErrSessionNotFound 的错误；Delete 必须是幂等的。
// 实现还必须可以安全地被多个 goroutine 调用，并且不得保留调用方传入的可变切片。
// 所有方法必须及时响应 context 取消与截止时间。
// Save 使用 Session.Revision 进行乐观并发控制：新会话必须为 0，已有会话必须等于当前版本；
// 存储成功后持久化版本加一，但不修改调用方传入的 Session。
type SessionStore interface {
	Load(ctx context.Context, id string) (*Session, error)
	Save(ctx context.Context, session *Session) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]SessionInfo, error)
}

// MemorySessionStore 是并发安全的内存会话存储，适合测试和单进程服务。
type MemorySessionStore struct {
	mu          sync.RWMutex
	sessions    map[string]*Session
	checkpoints *MemoryCheckpointStore
	goals       *MemoryGoalStore
	toolResults *MemoryToolResultStore
}

var (
	_ SessionStore            = (*MemorySessionStore)(nil)
	_ CheckpointStoreProvider = (*MemorySessionStore)(nil)
	_ GoalStoreProvider       = (*MemorySessionStore)(nil)
	_ ToolResultStoreProvider = (*MemorySessionStore)(nil)
)

// NewMemorySessionStore 创建内存会话存储。
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		sessions:    make(map[string]*Session),
		checkpoints: NewMemoryCheckpointStore(),
		goals:       NewMemoryGoalStore(),
		toolResults: NewMemoryToolResultStore(),
	}
}

// CheckpointStore 返回与会话共享生命周期的内存检查点存储。
func (s *MemorySessionStore) CheckpointStore() CheckpointStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkpoints == nil {
		s.checkpoints = NewMemoryCheckpointStore()
	}
	return s.checkpoints
}

// GoalStore 返回与会话共享生命周期的内存目标存储。
func (s *MemorySessionStore) GoalStore() GoalStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goals == nil {
		s.goals = NewMemoryGoalStore()
	}
	return s.goals
}

// ToolResultStore 返回与会话配套的内存大型工具结果存储。
func (s *MemorySessionStore) ToolResultStore() ToolResultStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolResults == nil {
		s.toolResults = NewMemoryToolResultStore()
	}
	return s.toolResults
}

// Load 加载会话快照。
func (s *MemorySessionStore) Load(ctx context.Context, id string) (*Session, error) {
	if err := validateSessionContextAndID(ctx, id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	session, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	return cloneSession(session), nil
}

// Save 保存并完全替换会话快照，拒绝覆盖更新版本。
func (s *MemorySessionStore) Save(ctx context.Context, session *Session) error {
	if err := validateSession(ctx, session); err != nil {
		return err
	}
	cloned := normalizedSession(session)
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = make(map[string]*Session)
	}
	if current := s.sessions[session.ID]; current != nil {
		if session.Revision != current.Revision {
			s.mu.Unlock()
			return fmt.Errorf("%w: session %q has revision %d, current revision is %d",
				ErrSessionConflict, session.ID, session.Revision, current.Revision)
		}
	} else if session.Revision != 0 {
		s.mu.Unlock()
		return fmt.Errorf("%w: session %q does not exist at revision %d",
			ErrSessionConflict, session.ID, session.Revision)
	}
	cloned.Revision++
	s.sessions[session.ID] = cloned
	s.mu.Unlock()
	return nil
}

// Delete 删除会话及其配套的检查点、目标和工具结果。会话不存在时也会清理可识别的孤儿资源。
func (s *MemorySessionStore) Delete(ctx context.Context, id string) error {
	if err := validateSessionContextAndID(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var checkpointID string
	if session := s.sessions[id]; session != nil {
		checkpointID = session.CheckpointID
	}
	var checkpoints CheckpointDeleter
	if s.checkpoints != nil {
		checkpoints = s.checkpoints
	}
	var goals GoalStore
	if s.goals != nil {
		goals = s.goals
	}
	var toolResults ToolResultStore
	if s.toolResults != nil {
		toolResults = s.toolResults
	}
	if err := deleteSessionResources(ctx, id, checkpointID, checkpoints, goals, toolResults); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(s.sessions, id)
	return nil
}

// List 按更新时间从新到旧列出会话。
func (s *MemorySessionStore) List(ctx context.Context) ([]SessionInfo, error) {
	if ctx == nil {
		return nil, errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	infos := make([]SessionInfo, 0, len(s.sessions))
	for _, session := range s.sessions {
		infos = append(infos, sessionInfo(session))
	}
	s.mu.RUnlock()
	sortSessionInfos(infos)
	return infos, nil
}

func validateSessionContextAndID(ctx context.Context, id string) error {
	if ctx == nil {
		return errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return validateManagedSessionID(id)
}

func validateManagedSessionID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("agentkit: session ID is required")
	}
	if id != strings.TrimSpace(id) {
		return errors.New("agentkit: session ID must not have surrounding whitespace")
	}
	return nil
}

func validateSession(ctx context.Context, session *Session) error {
	if session == nil {
		return errors.New("agentkit: session is required")
	}
	if err := validateSessionContextAndID(ctx, session.ID); err != nil {
		return err
	}
	return nil
}

func cloneSession(session *Session) *Session {
	if session == nil {
		return nil
	}
	cloned := *session
	cloned.Messages = cloneHistoryMessages(session.Messages)
	cloned.Context = cloneHistoryMessages(session.Context)
	cloned.PendingInterrupts = cloneInterruptPoints(session.PendingInterrupts)
	cloned.Tags = append([]string(nil), session.Tags...)
	return &cloned
}

func cloneSessionMetadata(metadata SessionMetadata) SessionMetadata {
	metadata.Tags = append([]string(nil), metadata.Tags...)
	return metadata
}

func normalizedSession(session *Session) *Session {
	cloned := cloneSession(session)
	now := time.Now().UTC()
	if cloned.CreatedAt.IsZero() {
		cloned.CreatedAt = now
	}
	if cloned.UpdatedAt.IsZero() {
		cloned.UpdatedAt = cloned.CreatedAt
	}
	return cloned
}

func sessionInfo(session *Session) SessionInfo {
	contextCount := len(session.Context)
	if session.Context == nil {
		contextCount = len(session.Messages)
	}
	return SessionInfo{
		SessionMetadata: SessionMetadata{
			Title:   session.Title,
			OwnerID: session.OwnerID,
			Tags:    append([]string(nil), session.Tags...),
		},
		ID:                    session.ID,
		CreatedAt:             session.CreatedAt,
		UpdatedAt:             session.UpdatedAt,
		MessageCount:          len(session.Messages),
		ContextMessageCount:   contextCount,
		PendingInterruptCount: len(session.PendingInterrupts),
		Archived:              session.Archived,
		Revision:              session.Revision,
	}
}

func sortSessionInfos(infos []SessionInfo) {
	sort.SliceStable(infos, func(i, j int) bool {
		if infos[i].UpdatedAt.Equal(infos[j].UpdatedAt) {
			return infos[i].ID < infos[j].ID
		}
		return infos[i].UpdatedAt.After(infos[j].UpdatedAt)
	})
}

func sessionStorageKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
