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
		if err := checkpoints.Delete(ctx, checkpointID); err != nil {
			errs = append(errs, fmt.Errorf("agentkit: delete checkpoint for session %q: %w", sessionID, err))
		}
	}
	if goals != nil {
		infos, err := goals.List(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("agentkit: list goals for session %q: %w", sessionID, err))
		} else {
			for _, info := range infos {
				if info.SessionID != sessionID {
					continue
				}
				if err := goals.Delete(ctx, info.ID); err != nil {
					errs = append(errs, fmt.Errorf("agentkit: delete goal %q for session %q: %w", info.ID, sessionID, err))
				}
			}
		}
	}
	if toolResults != nil {
		infos, err := toolResults.List(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("agentkit: list tool results for session %q: %w", sessionID, err))
		} else {
			for _, info := range infos {
				if info.SessionID != sessionID {
					continue
				}
				if err := toolResults.Delete(ctx, info.ID); err != nil {
					errs = append(errs, fmt.Errorf("agentkit: delete tool result %q for session %q: %w", info.ID, sessionID, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}
