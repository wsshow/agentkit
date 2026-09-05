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
	// ErrGoalNotFound 表示目标存储中不存在指定目标。
	ErrGoalNotFound = errors.New("agentkit: goal not found")
	// ErrGoalConflict 表示目标已被其他调用方更新，当前快照不能覆盖新状态。
	ErrGoalConflict = errors.New("agentkit: goal revision conflict")
)

// GoalStatus 描述持久化目标的生命周期状态。
type GoalStatus string

const (
	GoalStatusActive    GoalStatus = "active"
	GoalStatusPaused    GoalStatus = "paused"
	GoalStatusCompleted GoalStatus = "completed"
	GoalStatusBlocked   GoalStatus = "blocked"
)

// Goal 是一个可跨进程重启恢复的目标快照。
// LastResponse、LastReason 和 NextPrompt 用于在每个执行步骤之间恢复自动推进状态。
type Goal struct {
	ID                  string     `json:"id"`
	SessionID           string     `json:"session_id"`
	Objective           string     `json:"objective"`
	SuccessCriteria     string     `json:"success_criteria,omitempty"`
	Status              GoalStatus `json:"status"`
	Iteration           int        `json:"iteration"`
	MaxIterations       int        `json:"max_iterations"`
	LastResponse        string     `json:"last_response,omitempty"`
	LastReason          string     `json:"last_reason,omitempty"`
	NextPrompt          string     `json:"next_prompt,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	InProgress          bool       `json:"in_progress,omitempty"`
	AwaitingInterrupt   bool       `json:"awaiting_interrupt,omitempty"`
	PendingEvaluation   bool       `json:"pending_evaluation,omitempty"`
	AttemptIteration    int        `json:"attempt_iteration,omitempty"`
	HistoryMessageCount int        `json:"history_message_count,omitempty"`
	PendingPrompt       string     `json:"pending_prompt,omitempty"`
	Revision            uint64     `json:"revision"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// GoalInfo 是用于目标列表展示的轻量元数据。
type GoalInfo struct {
	ID                string     `json:"id"`
	SessionID         string     `json:"session_id"`
	Objective         string     `json:"objective"`
	Status            GoalStatus `json:"status"`
	Iteration         int        `json:"iteration"`
	MaxIterations     int        `json:"max_iterations"`
	AttemptIteration  int        `json:"attempt_iteration,omitempty"`
	InProgress        bool       `json:"in_progress,omitempty"`
	AwaitingInterrupt bool       `json:"awaiting_interrupt,omitempty"`
	PendingEvaluation bool       `json:"pending_evaluation,omitempty"`
	LastReason        string     `json:"last_reason,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	Revision          uint64     `json:"revision"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// GoalStore 管理持久化目标。
// Load 在目标不存在时必须返回包装 ErrGoalNotFound 的错误；Delete 必须是幂等的。
// 实现还必须可以安全地被多个 goroutine 调用，并且不得保留调用方传入的可变数据。
// 所有方法必须及时响应 context 取消与截止时间。
// Save 使用 Goal.Revision 进行乐观并发控制：新目标必须为 0，已有目标必须等于当前版本；
// 存储成功后持久化版本加一，但不修改调用方传入的 Goal。
type GoalStore interface {
	Load(ctx context.Context, id string) (*Goal, error)
	Save(ctx context.Context, goal *Goal) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]GoalInfo, error)
}

// GoalStoreProvider 允许会话存储提供共享生命周期的目标存储。
type GoalStoreProvider interface {
	GoalStore() GoalStore
}

// MemoryGoalStore 是并发安全的内存目标存储，适合测试和单进程服务。
type MemoryGoalStore struct {
	mu     sync.RWMutex
	goals  map[string]*Goal
	leases map[string]*GoalLease
	now    func() time.Time
}

var (
	_ GoalStore      = (*MemoryGoalStore)(nil)
	_ GoalLeaseStore = (*MemoryGoalStore)(nil)
)

// NewMemoryGoalStore 创建内存目标存储。
func NewMemoryGoalStore() *MemoryGoalStore {
	return &MemoryGoalStore{
		goals:  make(map[string]*Goal),
		leases: make(map[string]*GoalLease),
	}
}

// Load 加载目标快照。
func (s *MemoryGoalStore) Load(ctx context.Context, id string) (*Goal, error) {
	if err := validateGoalContextAndID(ctx, id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	goal, ok := s.goals[id]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrGoalNotFound, id)
	}
	return cloneGoal(goal), nil
}

// Save 保存并完全替换目标快照。
func (s *MemoryGoalStore) Save(ctx context.Context, goal *Goal) error {
	if err := validateGoal(ctx, goal); err != nil {
		return err
	}
	cloned := normalizedGoal(goal)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(goal, cloned)
}

func (s *MemoryGoalStore) saveLocked(goal, cloned *Goal) error {
	if s.goals == nil {
		s.goals = make(map[string]*Goal)
	}
	if current := s.goals[goal.ID]; current != nil {
		if goal.Revision != current.Revision {
			return fmt.Errorf("%w: goal %q has revision %d, current revision is %d",
				ErrGoalConflict, goal.ID, goal.Revision, current.Revision)
		}
	} else if goal.Revision != 0 {
		return fmt.Errorf("%w: goal %q does not exist at revision %d",
			ErrGoalConflict, goal.ID, goal.Revision)
	}
	cloned.Revision++
	s.goals[goal.ID] = cloned
	return nil
}

// Delete 删除目标。目标不存在时也返回 nil。
func (s *MemoryGoalStore) Delete(ctx context.Context, id string) error {
	if err := validateGoalContextAndID(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.goals, id)
	delete(s.leases, id)
	s.mu.Unlock()
	return nil
}

// List 按更新时间从新到旧列出目标。
func (s *MemoryGoalStore) List(ctx context.Context) ([]GoalInfo, error) {
	if ctx == nil {
		return nil, errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	infos := make([]GoalInfo, 0, len(s.goals))
	for _, goal := range s.goals {
		infos = append(infos, goalInfo(goal))
	}
	s.mu.RUnlock()
	sortGoalInfos(infos)
	return infos, nil
}

func validateGoalContextAndID(ctx context.Context, id string) error {
	if ctx == nil {
		return errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("agentkit: goal ID is required")
	}
	return nil
}

func validateGoal(ctx context.Context, goal *Goal) error {
	if goal == nil {
		return errors.New("agentkit: goal is required")
	}
	if err := validateGoalContextAndID(ctx, goal.ID); err != nil {
		return err
	}
	if strings.TrimSpace(goal.SessionID) == "" {
		return errors.New("agentkit: goal session ID is required")
	}
	if strings.TrimSpace(goal.Objective) == "" {
		return errors.New("agentkit: goal objective is required")
	}
	if goal.Iteration < 0 {
		return fmt.Errorf("agentkit: goal iteration must not be negative: %d", goal.Iteration)
	}
	if goal.AttemptIteration < 0 {
		return fmt.Errorf("agentkit: goal attempt iteration must not be negative: %d", goal.AttemptIteration)
	}
	if goal.HistoryMessageCount < 0 {
		return fmt.Errorf("agentkit: goal history message count must not be negative: %d", goal.HistoryMessageCount)
	}
	if goal.MaxIterations <= 0 {
		return fmt.Errorf("agentkit: goal max iterations must be positive: %d", goal.MaxIterations)
	}
	if !validGoalStatus(goal.Status) {
		return fmt.Errorf("agentkit: invalid goal status %q", goal.Status)
	}
	return nil
}

func validGoalStatus(status GoalStatus) bool {
	switch status {
	case GoalStatusActive, GoalStatusPaused, GoalStatusCompleted, GoalStatusBlocked:
		return true
	default:
		return false
	}
}

func normalizedGoal(goal *Goal) *Goal {
	cloned := cloneGoal(goal)
	now := time.Now().UTC()
	if cloned.CreatedAt.IsZero() {
		cloned.CreatedAt = now
	}
	if cloned.UpdatedAt.IsZero() {
		cloned.UpdatedAt = cloned.CreatedAt
	}
	return cloned
}

func cloneGoal(goal *Goal) *Goal {
	if goal == nil {
		return nil
	}
	cloned := *goal
	return &cloned
}

func goalInfo(goal *Goal) GoalInfo {
	return GoalInfo{
		ID:                goal.ID,
		SessionID:         goal.SessionID,
		Objective:         goal.Objective,
		Status:            goal.Status,
		Iteration:         goal.Iteration,
		MaxIterations:     goal.MaxIterations,
		AttemptIteration:  goal.AttemptIteration,
		InProgress:        goal.InProgress,
		AwaitingInterrupt: goal.AwaitingInterrupt,
		PendingEvaluation: goal.PendingEvaluation,
		LastReason:        goal.LastReason,
		LastError:         goal.LastError,
		Revision:          goal.Revision,
		UpdatedAt:         goal.UpdatedAt,
	}
}

func sortGoalInfos(infos []GoalInfo) {
	sort.SliceStable(infos, func(i, j int) bool {
		if infos[i].UpdatedAt.Equal(infos[j].UpdatedAt) {
			return infos[i].ID < infos[j].ID
		}
		return infos[i].UpdatedAt.After(infos[j].UpdatedAt)
	})
}
