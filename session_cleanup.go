package agentkit

import (
	"context"
	"errors"
	"fmt"
)

func deleteSessionResources(
	ctx context.Context,
	sessionID, checkpointID string,
	checkpoints CheckpointDeleter,
	goals GoalStore,
	toolResults ToolResultStore,
) error {
	var errs []error
	if checkpointID != "" && checkpoints != nil {
		if err := doPersistence("checkpoint delete", func() error { return checkpoints.Delete(ctx, checkpointID) }); err != nil {
			errs = append(errs, fmt.Errorf("agentkit: delete checkpoint for session %q: %w", sessionID, err))
		}
	}
	if goals != nil {
		infos, err := goalStoreList(ctx, goals)
		if err != nil {
			errs = append(errs, fmt.Errorf("agentkit: list goals for session %q: %w", sessionID, err))
		} else {
			for _, info := range infos {
				if info.SessionID != sessionID {
					continue
				}
				if err := goalStoreDelete(ctx, goals, info.ID); err != nil {
					errs = append(errs, fmt.Errorf("agentkit: delete goal %q for session %q: %w", info.ID, sessionID, err))
				}
			}
		}
	}
	if toolResults != nil {
		infos, err := toolResultStoreList(ctx, toolResults)
		if err != nil {
			errs = append(errs, fmt.Errorf("agentkit: list tool results for session %q: %w", sessionID, err))
		} else {
			for _, info := range infos {
				if info.SessionID != sessionID {
					continue
				}
				if err := toolResultStoreDelete(ctx, toolResults, info.ID); err != nil {
					errs = append(errs, fmt.Errorf("agentkit: delete tool result %q for session %q: %w", info.ID, sessionID, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}
