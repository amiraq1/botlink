package tools

import (
	"context"
	"errors"

	"github.com/amiraq1/botlink/internal/store"
)

// SyncBuiltinTools synchronizes all registered builtin tools into the store's
// tools table. If a tool doesn't exist, it is created. If it already exists,
// its description and schema are updated while preserving its enabled state.
func SyncBuiltinTools(ctx context.Context, st *store.Store, nowMs int64) error {
	for _, t := range List() {
		input := store.ToolInput{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.Schema(),
			TimeoutMs:   5000,
			Enabled:     true,
			Kind:        store.ToolKindBuiltin,
		}

		existing, err := st.GetToolByName(ctx, t.Name())
		if errors.Is(err, store.ErrNotFound) {
			if _, err := st.CreateTool(ctx, input, nowMs); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			input.Enabled = existing.Enabled
			input.TimeoutMs = existing.TimeoutMs
			if err := st.UpdateTool(ctx, existing.ID, input, nowMs); err != nil {
				return err
			}
		}
	}
	return nil
}
