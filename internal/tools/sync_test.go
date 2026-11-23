package tools_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/amiraq1/botlink/internal/store"
	"github.com/amiraq1/botlink/internal/tools"
	_ "github.com/amiraq1/botlink/internal/tools/builtin" // triggers builtin registration
)

func TestSyncBuiltinTools(t *testing.T) {
	key := make([]byte, 32)
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), key)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// 1. Initial sync creates the builtin tools in DB
	if err := tools.SyncBuiltinTools(ctx, st, 1000); err != nil {
		t.Fatalf("SyncBuiltinTools: %v", err)
	}

	tTime, err := st.GetToolByName(ctx, "get_time")
	if err != nil {
		t.Fatalf("get_time not found: %v", err)
	}
	if tTime.Kind != store.ToolKindBuiltin || !tTime.Enabled {
		t.Errorf("get_time unexpected: %+v", tTime)
	}

	tCalc, err := st.GetToolByName(ctx, "calculate")
	if err != nil {
		t.Fatalf("calculate not found: %v", err)
	}
	if tCalc.Kind != store.ToolKindBuiltin || !tCalc.Enabled {
		t.Errorf("calculate unexpected: %+v", tCalc)
	}

	// 2. Disable calculate in DB, then re-sync; enabled state must be preserved
	if err := st.EnableTool(ctx, tCalc.ID, false, 1500); err != nil {
		t.Fatalf("EnableTool: %v", err)
	}

	if err := tools.SyncBuiltinTools(ctx, st, 2000); err != nil {
		t.Fatalf("second SyncBuiltinTools: %v", err)
	}

	tCalcAfter, err := st.GetToolByName(ctx, "calculate")
	if err != nil {
		t.Fatalf("get calculate after sync: %v", err)
	}
	if tCalcAfter.Enabled {
		t.Error("SyncBuiltinTools should preserve existing enabled=false state")
	}
}
