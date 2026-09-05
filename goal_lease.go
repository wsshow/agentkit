package agentkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrGoalLeaseHeld 表示目标正由另一个 worker 的有效租约持有。
	ErrGoalLeaseHeld = errors.New("agentkit: goal lease is held")
	// ErrGoalLeaseLost 表示租约已过期、被接管或令牌不再匹配。
	ErrGoalLeaseLost     = errors.New("agentkit: goal lease is lost")
	errGoalLeaseNotFound = errors.New("agentkit: goal lease not found")
)

// GoalLease 是一次有期限的目标执行所有权。Token 是不可猜测的 fencing 令牌。
type GoalLease struct {
	GoalID    string    `json:"goal_id"`
	WorkerID  string    `json:"worker_id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GoalLeaseHeldError 提供当前持有者和到期时间，便于调度器安全安排重试。
// 可同时通过 errors.Is(err, ErrGoalLeaseHeld) 和 errors.As 使用。
type GoalLeaseHeldError struct {
	Lease GoalLease
}

// Error 返回包含持有者和到期时间的租约冲突信息。
func (e *GoalLeaseHeldError) Error() string {
	if e == nil {
		return ErrGoalLeaseHeld.Error()
	}
	return fmt.Sprintf("%s: goal %q is owned by worker %q until %s",
		ErrGoalLeaseHeld, e.Lease.GoalID, e.Lease.WorkerID, e.Lease.ExpiresAt.Format(time.RFC3339Nano))
}

// Unwrap 支持 errors.Is(err, ErrGoalLeaseHeld)。
func (e *GoalLeaseHeldError) Unwrap() error {
	return ErrGoalLeaseHeld
}

// GoalLeaseStore 为 GoalStore 增加原子 worker 所有权和防陈旧写入能力。
// AcquireGoalLease 对未过期租约必须返回包装 ErrGoalLeaseHeld 的错误；过期租约可被接管。
// RenewGoalLease、SaveGoalWithLease 和 DeleteGoalWithLease 必须校验有效 Token，
// 不匹配或已过期时返回包装 ErrGoalLeaseLost 的错误。ReleaseGoalLease 不得释放其他 Token。
// 所有方法必须可以安全地被多个 goroutine 调用，并及时响应 context 取消与截止时间。
type GoalLeaseStore interface {
	AcquireGoalLease(ctx context.Context, goalID, workerID string, duration time.Duration) (*GoalLease, error)
	RenewGoalLease(ctx context.Context, lease *GoalLease, duration time.Duration) (*GoalLease, error)
	ReleaseGoalLease(ctx context.Context, lease *GoalLease) error
	SaveGoalWithLease(ctx context.Context, goal *Goal, lease *GoalLease) error
	DeleteGoalWithLease(ctx context.Context, goalID string, lease *GoalLease) error
}

// AcquireGoalLease 原子地取得目标租约。
func (s *MemoryGoalStore) AcquireGoalLease(
	ctx context.Context,
	goalID, workerID string,
	duration time.Duration,
) (*GoalLease, error) {
	if err := validateGoalLeaseRequest(ctx, goalID, workerID, duration); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leases == nil {
		s.leases = make(map[string]*GoalLease)
	}
	now := s.leaseNow()
	if current := s.leases[goalID]; current != nil && current.ExpiresAt.After(now) {
		return nil, heldGoalLeaseError(current)
	}
	lease := &GoalLease{
		GoalID: goalID, WorkerID: workerID, Token: uuid.NewString(), ExpiresAt: now.Add(duration),
	}
	s.leases[goalID] = lease
	return cloneGoalLease(lease), nil
}

// RenewGoalLease 延长仍然有效的目标租约。
func (s *MemoryGoalStore) RenewGoalLease(
	ctx context.Context,
	lease *GoalLease,
	duration time.Duration,
) (*GoalLease, error) {
	if err := validateGoalLeaseAndDuration(ctx, lease, duration); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.validLeaseLocked(lease)
	if err != nil {
		return nil, err
	}
	renewed := cloneGoalLease(current)
	renewed.ExpiresAt = s.leaseNow().Add(duration)
	s.leases[lease.GoalID] = renewed
	return cloneGoalLease(renewed), nil
}

// ReleaseGoalLease 释放自己持有的目标租约；同一令牌重复释放是幂等的。
func (s *MemoryGoalStore) ReleaseGoalLease(ctx context.Context, lease *GoalLease) error {
	if err := validateGoalLease(ctx, lease); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.leases[lease.GoalID]
	if current == nil {
		return nil
	}
	if current.Token != lease.Token {
		return lostGoalLeaseError(lease)
	}
	delete(s.leases, lease.GoalID)
	return nil
}

// SaveGoalWithLease 在同一临界区校验租约、目标版本并保存目标。
func (s *MemoryGoalStore) SaveGoalWithLease(ctx context.Context, goal *Goal, lease *GoalLease) error {
	if err := validateGoal(ctx, goal); err != nil {
		return err
	}
	if err := validateMatchingGoalLease(ctx, goal.ID, lease); err != nil {
		return err
	}
	cloned := normalizedGoal(goal)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.validLeaseLocked(lease); err != nil {
		return err
	}
	return s.saveLocked(goal, cloned)
}

// DeleteGoalWithLease 在租约仍有效时删除目标；目标不存在时也返回 nil。
func (s *MemoryGoalStore) DeleteGoalWithLease(ctx context.Context, goalID string, lease *GoalLease) error {
	if err := validateGoalContextAndID(ctx, goalID); err != nil {
		return err
	}
	if err := validateMatchingGoalLease(ctx, goalID, lease); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.validLeaseLocked(lease); err != nil {
		return err
	}
	delete(s.goals, goalID)
	delete(s.leases, goalID)
	return nil
}

func (s *MemoryGoalStore) validLeaseLocked(lease *GoalLease) (*GoalLease, error) {
	current := s.leases[lease.GoalID]
	if current == nil || current.Token != lease.Token || !current.ExpiresAt.After(s.leaseNow()) {
		return nil, lostGoalLeaseError(lease)
	}
	return current, nil
}

func (s *MemoryGoalStore) leaseNow() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func validateGoalLeaseRequest(
	ctx context.Context,
	goalID, workerID string,
	duration time.Duration,
) error {
	if err := validateGoalContextAndID(ctx, goalID); err != nil {
		return err
	}
	if strings.TrimSpace(workerID) == "" {
		return errors.New("agentkit: goal lease worker ID is required")
	}
	if workerID != strings.TrimSpace(workerID) {
		return fmt.Errorf("agentkit: goal lease worker ID must not have surrounding whitespace: %q", workerID)
	}
	if duration <= 0 {
		return fmt.Errorf("agentkit: goal lease duration must be positive: %s", duration)
	}
	return nil
}

func validateGoalLeaseAndDuration(ctx context.Context, lease *GoalLease, duration time.Duration) error {
	if err := validateGoalLease(ctx, lease); err != nil {
		return err
	}
	if duration <= 0 {
		return fmt.Errorf("agentkit: goal lease duration must be positive: %s", duration)
	}
	return nil
}

func validateMatchingGoalLease(ctx context.Context, goalID string, lease *GoalLease) error {
	if err := validateGoalLease(ctx, lease); err != nil {
		return err
	}
	if lease.GoalID != goalID {
		return fmt.Errorf("agentkit: goal lease belongs to %q, not %q", lease.GoalID, goalID)
	}
	return nil
}

func validateGoalLease(ctx context.Context, lease *GoalLease) error {
	if lease == nil {
		return errors.New("agentkit: goal lease is required")
	}
	if err := validateGoalContextAndID(ctx, lease.GoalID); err != nil {
		return err
	}
	if strings.TrimSpace(lease.WorkerID) == "" {
		return errors.New("agentkit: goal lease worker ID is required")
	}
	if lease.WorkerID != strings.TrimSpace(lease.WorkerID) {
		return fmt.Errorf("agentkit: goal lease worker ID must not have surrounding whitespace: %q", lease.WorkerID)
	}
	if strings.TrimSpace(lease.Token) == "" {
		return errors.New("agentkit: goal lease token is required")
	}
	if lease.Token != strings.TrimSpace(lease.Token) {
		return errors.New("agentkit: goal lease token must not have surrounding whitespace")
	}
	if lease.ExpiresAt.IsZero() {
		return errors.New("agentkit: goal lease expiration is required")
	}
	return nil
}

func heldGoalLeaseError(lease *GoalLease) error {
	if lease == nil {
		return ErrGoalLeaseHeld
	}
	return &GoalLeaseHeldError{Lease: *cloneGoalLease(lease)}
}

func lostGoalLeaseError(lease *GoalLease) error {
	goalID := ""
	if lease != nil {
		goalID = lease.GoalID
	}
	return fmt.Errorf("%w: goal %q", ErrGoalLeaseLost, goalID)
}

func cloneGoalLease(lease *GoalLease) *GoalLease {
	if lease == nil {
		return nil
	}
	cloned := *lease
	return &cloned
}
