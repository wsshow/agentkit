package agentkit

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrPersistencePanic 表示自定义持久化后端在执行操作时发生 panic。
	ErrPersistencePanic = errors.New("agentkit: persistence backend panicked")
	// ErrInvalidPersistenceData 表示自定义持久化后端返回 nil、错误 ID 或无效快照。
	ErrInvalidPersistenceData = errors.New("agentkit: persistence backend returned invalid data")
)

func callPersistence[T any](operation string, call func() (T, error)) (value T, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			var zero T
			value = zero
			err = fmt.Errorf("%w during %s: %v", ErrPersistencePanic, operation, recovered)
		}
	}()
	return call()
}

func doPersistence(operation string, call func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w during %s: %v", ErrPersistencePanic, operation, recovered)
		}
	}()
	return call()
}

func sessionStoreLoad(ctx context.Context, store SessionStore, id string) (*Session, error) {
	session, err := callPersistence("session load", func() (*Session, error) { return store.Load(ctx, id) })
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("%w: session store returned nil for %q", ErrInvalidPersistenceData, id)
	}
	if session.ID != id {
		return nil, fmt.Errorf("%w: session store returned %q for requested session %q", ErrInvalidPersistenceData, session.ID, id)
	}
	if err := validateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("%w: invalid session %q: %w", ErrInvalidPersistenceData, id, err)
	}
	return cloneSession(session), nil
}

func sessionStoreSave(ctx context.Context, store SessionStore, session *Session) error {
	return doPersistence("session save", func() error { return store.Save(ctx, session) })
}

func sessionStoreDelete(ctx context.Context, store SessionStore, id string) error {
	return doPersistence("session delete", func() error { return store.Delete(ctx, id) })
}

func sessionStoreList(ctx context.Context, store SessionStore) ([]SessionInfo, error) {
	infos, err := callPersistence("session list", func() ([]SessionInfo, error) { return store.List(ctx) })
	if err != nil {
		return nil, err
	}
	var cloned []SessionInfo
	if infos != nil {
		cloned = make([]SessionInfo, len(infos))
	}
	seen := make(map[string]struct{}, len(infos))
	for index, info := range infos {
		if err := validateSessionInfo(info); err != nil {
			return nil, fmt.Errorf("%w: invalid session list entry %d: %w",
				ErrInvalidPersistenceData, index, err)
		}
		if _, exists := seen[info.ID]; exists {
			return nil, fmt.Errorf("%w: session list returned duplicate ID %q",
				ErrInvalidPersistenceData, info.ID)
		}
		seen[info.ID] = struct{}{}
		info.Tags = append([]string(nil), info.Tags...)
		cloned[index] = info
	}
	return cloned, nil
}

func goalStoreLoad(ctx context.Context, store GoalStore, id string) (*Goal, error) {
	goal, err := callPersistence("goal load", func() (*Goal, error) { return store.Load(ctx, id) })
	if err != nil {
		return nil, err
	}
	if goal == nil {
		return nil, fmt.Errorf("%w: goal store returned nil for %q", ErrInvalidPersistenceData, id)
	}
	if goal.ID != id {
		return nil, fmt.Errorf("%w: goal store returned %q for requested goal %q", ErrInvalidPersistenceData, goal.ID, id)
	}
	if err := validateGoal(ctx, goal); err != nil {
		return nil, fmt.Errorf("%w: invalid goal %q: %w", ErrInvalidPersistenceData, id, err)
	}
	return cloneGoal(goal), nil
}

func goalStoreSave(ctx context.Context, store GoalStore, goal *Goal) error {
	return doPersistence("goal save", func() error { return store.Save(ctx, goal) })
}

func goalStoreDelete(ctx context.Context, store GoalStore, id string) error {
	return doPersistence("goal delete", func() error { return store.Delete(ctx, id) })
}

func goalStoreList(ctx context.Context, store GoalStore) ([]GoalInfo, error) {
	infos, err := callPersistence("goal list", func() ([]GoalInfo, error) { return store.List(ctx) })
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(infos))
	for index, info := range infos {
		if err := validateGoalInfo(info); err != nil {
			return nil, fmt.Errorf("%w: invalid goal list entry %d: %w",
				ErrInvalidPersistenceData, index, err)
		}
		if _, exists := seen[info.ID]; exists {
			return nil, fmt.Errorf("%w: goal list returned duplicate ID %q",
				ErrInvalidPersistenceData, info.ID)
		}
		seen[info.ID] = struct{}{}
	}
	return append([]GoalInfo(nil), infos...), nil
}

func acquireGoalLease(
	ctx context.Context,
	store GoalLeaseStore,
	goalID, workerID string,
	duration time.Duration,
) (*GoalLease, error) {
	return callPersistence("goal lease acquire", func() (*GoalLease, error) {
		return store.AcquireGoalLease(ctx, goalID, workerID, duration)
	})
}

func renewGoalLease(
	ctx context.Context,
	store GoalLeaseStore,
	lease *GoalLease,
	duration time.Duration,
) (*GoalLease, error) {
	return callPersistence("goal lease renew", func() (*GoalLease, error) {
		return store.RenewGoalLease(ctx, lease, duration)
	})
}

func releaseStoredGoalLease(ctx context.Context, store GoalLeaseStore, lease *GoalLease) error {
	return doPersistence("goal lease release", func() error { return store.ReleaseGoalLease(ctx, lease) })
}

func saveGoalWithLease(ctx context.Context, store GoalLeaseStore, goal *Goal, lease *GoalLease) error {
	return doPersistence("goal save with lease", func() error { return store.SaveGoalWithLease(ctx, goal, lease) })
}

func deleteGoalWithLease(ctx context.Context, store GoalLeaseStore, goalID string, lease *GoalLease) error {
	return doPersistence("goal delete with lease", func() error { return store.DeleteGoalWithLease(ctx, goalID, lease) })
}

func toolResultStoreLoad(ctx context.Context, store ToolResultStore, id string) (*StoredToolResult, error) {
	result, err := callPersistence("tool result load", func() (*StoredToolResult, error) { return store.Load(ctx, id) })
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("%w: tool result store returned nil for %q", ErrInvalidPersistenceData, id)
	}
	if result.ID != id {
		return nil, fmt.Errorf("%w: tool result store returned %q for requested result %q", ErrInvalidPersistenceData, result.ID, id)
	}
	if err := validateStoredToolResult(ctx, result); err != nil {
		return nil, fmt.Errorf("%w: invalid tool result %q: %w", ErrInvalidPersistenceData, id, err)
	}
	return cloneStoredToolResult(result), nil
}

func toolResultStoreSave(ctx context.Context, store ToolResultStore, result *StoredToolResult) error {
	return doPersistence("tool result save", func() error { return store.Save(ctx, result) })
}

func toolResultStoreDelete(ctx context.Context, store ToolResultStore, id string) error {
	return doPersistence("tool result delete", func() error { return store.Delete(ctx, id) })
}

func toolResultStoreList(ctx context.Context, store ToolResultStore) ([]ToolResultInfo, error) {
	infos, err := callPersistence("tool result list", func() ([]ToolResultInfo, error) { return store.List(ctx) })
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(infos))
	for index, info := range infos {
		if err := validateToolResultInfo(info); err != nil {
			return nil, fmt.Errorf("%w: invalid tool result list entry %d: %w",
				ErrInvalidPersistenceData, index, err)
		}
		if _, exists := seen[info.ID]; exists {
			return nil, fmt.Errorf("%w: tool result list returned duplicate ID %q",
				ErrInvalidPersistenceData, info.ID)
		}
		seen[info.ID] = struct{}{}
	}
	return append([]ToolResultInfo(nil), infos...), nil
}

type guardedCheckpointStore struct {
	store CheckpointStore
}

func (s *guardedCheckpointStore) Set(ctx context.Context, id string, value []byte) error {
	return doPersistence("checkpoint save", func() error { return s.store.Set(ctx, id, value) })
}

func (s *guardedCheckpointStore) Get(ctx context.Context, id string) (value []byte, existed bool, err error) {
	type lookup struct {
		value   []byte
		existed bool
	}
	result, err := callPersistence("checkpoint load", func() (lookup, error) {
		value, existed, err := s.store.Get(ctx, id)
		return lookup{value: value, existed: existed}, err
	})
	return result.value, result.existed, err
}

type guardedCheckpointStoreWithDelete struct {
	*guardedCheckpointStore
	deleter CheckpointDeleter
}

func (s *guardedCheckpointStoreWithDelete) Delete(ctx context.Context, id string) error {
	return doPersistence("checkpoint delete", func() error { return s.deleter.Delete(ctx, id) })
}

func guardCheckpointStore(store CheckpointStore) CheckpointStore {
	if store == nil {
		return nil
	}
	guarded := &guardedCheckpointStore{store: store}
	if deleter, ok := store.(CheckpointDeleter); ok {
		return &guardedCheckpointStoreWithDelete{guardedCheckpointStore: guarded, deleter: deleter}
	}
	return guarded
}

func providedStore[T any](operation string, provider func() T) (T, error) {
	return callPersistence(operation, func() (T, error) { return provider(), nil })
}
