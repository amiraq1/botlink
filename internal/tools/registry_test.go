package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/amiraq1/botlink/internal/tools"
)

type dummyTool struct {
	name string
}

func (d *dummyTool) Name() string {
	return d.name
}

func (d *dummyTool) Description() string {
	return "dummy description"
}

func (d *dummyTool) Schema() json.RawMessage {
	return json.RawMessage(`{}`)
}

func (d *dummyTool) Execute(ctx context.Context, in json.RawMessage) (tools.Result, error) {
	return tools.Result{Text: "dummy"}, nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := tools.NewRegistry()
	tool := &dummyTool{name: "test_tool"}

	if err := r.Register(tool); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Duplicate register should fail
	if err := r.Register(tool); !errors.Is(err, tools.ErrToolAlreadyExists) {
		t.Errorf("expected ErrToolAlreadyExists, got %v", err)
	}

	// Get registered tool
	got, ok := r.Get("test_tool")
	if !ok || got == nil {
		t.Fatalf("expected tool, got ok=%v, tool=%v", ok, got)
	}
	if got.Name() != "test_tool" {
		t.Errorf("got name %q, want 'test_tool'", got.Name())
	}

	// Get nonexistent
	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("expected ok=false for nonexistent tool")
	}

	// List
	list := r.List()
	if len(list) != 1 {
		t.Errorf("expected 1 tool in list, got %d", len(list))
	}
}

func TestRegistry_Validation(t *testing.T) {
	r := tools.NewRegistry()

	if err := r.Register(nil); err == nil {
		t.Error("expected error on registering nil tool")
	}

	empty := &dummyTool{name: ""}
	if err := r.Register(empty); err == nil {
		t.Error("expected error on registering empty name tool")
	}
}

func TestRegistry_Concurrent(t *testing.T) {
	r := tools.NewRegistry()
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			toolName := "tool_" + string(rune('a'+idx))
			_ = r.Register(&dummyTool{name: toolName})
			_, _ = r.Get(toolName)
			_ = r.List()
		}(i)
	}
	wg.Wait()
}
