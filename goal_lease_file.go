package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
)

const fileGoalLeaseVersion = 1

type storedGoalLease struct {
	Version int        `json:"version"`
	Lease   *GoalLease `json:"lease"`
}

// AcquireGoalLease 原子地取得文件目标存储中的租约。
func (s *FileGoalStore) AcquireGoalLease(
	ctx context.Context,
	goalID, workerID string,
	duration time.Duration,
) (*GoalLease, error) {
	if err := validateGoalLeaseRequest(ctx, goalID, workerID, duration); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadLease(goalID)
	if err != nil && !errors.Is(err, errGoalLeaseNotFound) {
		return nil, err
	}
	now := s.leaseNow()
	if current != nil && current.ExpiresAt.After(now) {
		return nil, heldGoalLeaseError(current)
	}
	lease := &GoalLease{
		GoalID: goalID, WorkerID: workerID, Token: uuid.NewString(), ExpiresAt: now.Add(duration),
	}
	if err := s.writeLeaseLocked(lease); err != nil {
		return nil, err
	}
	return cloneGoalLease(lease), nil
}

// RenewGoalLease 延长仍然有效的文件目标租约。
func (s *FileGoalStore) RenewGoalLease(
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
	if err := s.writeLeaseLocked(renewed); err != nil {
		return nil, err
	}
	return cloneGoalLease(renewed), nil
}

// ReleaseGoalLease 释放自己持有的文件目标租约；同一令牌重复释放是幂等的。
func (s *FileGoalStore) ReleaseGoalLease(ctx context.Context, lease *GoalLease) error {
	if err := validateGoalLease(ctx, lease); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadLease(lease.GoalID)
	if errors.Is(err, errGoalLeaseNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Token != lease.Token {
		return lostGoalLeaseError(lease)
	}
	if err := os.Remove(s.leasePath(lease.GoalID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agentkit: release goal lease %q: %w", lease.GoalID, err)
	}
	return nil
}

// SaveGoalWithLease 在同一临界区校验租约、目标版本并保存目标。
func (s *FileGoalStore) SaveGoalWithLease(ctx context.Context, goal *Goal, lease *GoalLease) error {
	if err := validateGoal(ctx, goal); err != nil {
		return err
	}
	if err := validateMatchingGoalLease(ctx, goal.ID, lease); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.validLeaseLocked(lease); err != nil {
		return err
	}
	return s.saveLocked(goal)
}

// DeleteGoalWithLease 在租约仍有效时删除目标；目标不存在时也返回 nil。
func (s *FileGoalStore) DeleteGoalWithLease(ctx context.Context, goalID string, lease *GoalLease) error {
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
	if err := os.Remove(s.path(goalID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agentkit: delete goal %q: %w", goalID, err)
	}
	return nil
}

func (s *FileGoalStore) validLeaseLocked(lease *GoalLease) (*GoalLease, error) {
	current, err := s.loadLease(lease.GoalID)
	if err != nil || current.Token != lease.Token || !current.ExpiresAt.After(s.leaseNow()) {
		return nil, lostGoalLeaseError(lease)
	}
	return current, nil
}

func (s *FileGoalStore) loadLease(goalID string) (*GoalLease, error) {
	data, err := os.ReadFile(s.leasePath(goalID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", errGoalLeaseNotFound, goalID)
	}
	if err != nil {
		return nil, fmt.Errorf("agentkit: load goal lease %q: %w", goalID, err)
	}
	var stored storedGoalLease
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("agentkit: decode goal lease %q: %w", goalID, err)
	}
	if stored.Version != fileGoalLeaseVersion {
		return nil, fmt.Errorf("agentkit: unsupported goal lease file version %d", stored.Version)
	}
	if err := validateGoalLease(context.TODO(), stored.Lease); err != nil {
		return nil, fmt.Errorf("agentkit: invalid goal lease %q: %w", goalID, err)
	}
	if stored.Lease.GoalID != goalID {
		return nil, fmt.Errorf("agentkit: goal lease file ID mismatch: got %q, want %q", stored.Lease.GoalID, goalID)
	}
	return cloneGoalLease(stored.Lease), nil
}

func (s *FileGoalStore) writeLeaseLocked(lease *GoalLease) error {
	data, err := json.MarshalIndent(storedGoalLease{Version: fileGoalLeaseVersion, Lease: lease}, "", "  ")
	if err != nil {
		return fmt.Errorf("agentkit: encode goal lease %q: %w", lease.GoalID, err)
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(s.dir, ".goal-lease-*.tmp")
	if err != nil {
		return fmt.Errorf("agentkit: create temporary goal lease file: %w", err)
	}
	tempName := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempName)
		}
	}()
	if _, err = temp.Write(data); err != nil {
		return fmt.Errorf("agentkit: write goal lease %q: %w", lease.GoalID, err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("agentkit: sync goal lease %q: %w", lease.GoalID, err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("agentkit: close goal lease %q: %w", lease.GoalID, err)
	}
	if err = os.Rename(tempName, s.leasePath(lease.GoalID)); err != nil {
		return fmt.Errorf("agentkit: commit goal lease %q: %w", lease.GoalID, err)
	}
	committed = true
	return nil
}

func (s *FileGoalStore) leasePath(goalID string) string {
	return s.path(goalID) + ".lease"
}

func (s *FileGoalStore) leaseNow() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}
