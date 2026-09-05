package agentkit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	goalLeaseReleaseTimeout       = 5 * time.Second
	goalLeaseHeartbeatStopMinWait = 100 * time.Millisecond
	goalLeaseHeartbeatStopMaxWait = 5 * time.Second
)

type goalLeaseHeartbeat struct {
	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}
	mu       sync.Mutex
	err      error
}

func (r *GoalRunner) startLeaseHeartbeat(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	lease *GoalLease,
) *goalLeaseHeartbeat {
	heartbeat := &goalLeaseHeartbeat{}
	if r.leaseStore == nil || lease == nil {
		return heartbeat
	}
	heartbeat.stopCh = make(chan struct{})
	heartbeat.done = make(chan struct{})
	interval := r.leaseDuration / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	go func() {
		defer close(heartbeat.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		current := cloneGoalLease(lease)
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.stopCh:
				return
			case <-ticker.C:
				renewCtx, renewCancel := context.WithTimeout(ctx, interval)
				renewed, err := renewGoalLease(renewCtx, r.leaseStore, current, r.leaseDuration)
				renewCancel()
				if err == nil {
					if renewed == nil || renewed.GoalID != current.GoalID || renewed.Token != current.Token ||
						!renewed.ExpiresAt.After(current.ExpiresAt) {
						err = fmt.Errorf("%w: lease store returned an invalid renewal", ErrGoalLeaseLost)
					} else {
						current = renewed
						continue
					}
				}
				if ctx.Err() != nil {
					return
				}
				if errors.Is(err, ErrGoalLeaseLost) || !time.Now().UTC().Before(current.ExpiresAt) {
					heartbeat.setError(fmt.Errorf("%w: renew goal %q: %w", ErrGoalLeaseLost, lease.GoalID, err))
					cancel(ErrGoalLeaseLost)
					return
				}
			}
		}
	}()
	return heartbeat
}

func (h *goalLeaseHeartbeat) stop(timeout time.Duration) error {
	if h == nil || h.done == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = goalLeaseHeartbeatStopMinWait
	}
	h.stopOnce.Do(func() { close(h.stopCh) })
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-h.done:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.err
	case <-timer.C:
		return fmt.Errorf("agentkit: stop goal lease heartbeat: %w", context.DeadlineExceeded)
	}
}

func (h *goalLeaseHeartbeat) setError(err error) {
	h.mu.Lock()
	if h.err == nil {
		h.err = err
	}
	h.mu.Unlock()
}

func (r *GoalRunner) releaseLease(ctx context.Context, lease *GoalLease) error {
	if r.leaseStore == nil || lease == nil {
		return nil
	}
	return releaseGoalLease(ctx, r.leaseStore, lease, goalLeaseReleaseTimeout)
}

func releaseGoalLease(ctx context.Context, store GoalLeaseStore, lease *GoalLease, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.TODO()
	}
	releaseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	released := make(chan error, 1)
	go func() {
		released <- releaseStoredGoalLease(releaseCtx, store, lease)
	}()
	select {
	case err := <-released:
		if err != nil {
			return fmt.Errorf("agentkit: release goal lease: %w", err)
		}
		return nil
	case <-releaseCtx.Done():
		return fmt.Errorf("agentkit: release goal lease: %w", releaseCtx.Err())
	}
}

func goalLeaseHeartbeatStopTimeout(leaseDuration time.Duration) time.Duration {
	timeout := leaseDuration / 3
	if timeout < goalLeaseHeartbeatStopMinWait {
		return goalLeaseHeartbeatStopMinWait
	}
	if timeout > goalLeaseHeartbeatStopMaxWait {
		return goalLeaseHeartbeatStopMaxWait
	}
	return timeout
}
