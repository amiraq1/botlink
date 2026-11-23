package builtin_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/amiraq1/botlink/internal/tools/builtin"
)

func TestGetTimeTool_DefaultUTC(t *testing.T) {
	tool := builtin.NewGetTimeTool()
	if tool.Name() != "get_time" {
		t.Errorf("expected 'get_time', got %s", tool.Name())
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
		t.Fatalf("failed to parse output JSON: %v, raw=%s", err, res.Text)
	}

	if out["timezone"] != "UTC" {
		t.Errorf("expected timezone UTC, got %v", out["timezone"])
	}
	if out["utc"] == nil || out["utc"] == "" {
		t.Error("expected non-empty utc timestamp")
	}
	if out["unix_timestamp"] == nil {
		t.Error("expected unix_timestamp")
	}
}

func TestGetTimeTool_SpecificTimezones(t *testing.T) {
	tool := builtin.NewGetTimeTool()
	ctx := context.Background()

	testZones := []string{
		"Africa/Cairo",
		"America/New_York",
		"Europe/London",
		"Asia/Tokyo",
	}

	for _, tz := range testZones {
		in, _ := json.Marshal(map[string]string{"timezone": tz})
		res, err := tool.Execute(ctx, in)
		if err != nil {
			t.Errorf("tz %s failed: %v", tz, err)
			continue
		}

		var out map[string]any
		if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
			t.Errorf("tz %s invalid JSON: %v", tz, err)
			continue
		}

		if out["timezone"] != tz {
			t.Errorf("expected timezone %s, got %v", tz, out["timezone"])
		}
	}
}

func TestGetTimeTool_Errors(t *testing.T) {
	tool := builtin.NewGetTimeTool()
	ctx := context.Background()

	// Invalid timezone name
	in, _ := json.Marshal(map[string]string{"timezone": "NonExistent/Mars_Base"})
	_, err := tool.Execute(ctx, in)
	if err == nil {
		t.Error("expected error for nonexistent timezone")
	}

	// Excessively long timezone
	longTz := strings.Repeat("A", 100)
	in, _ = json.Marshal(map[string]string{"timezone": longTz})
	_, err = tool.Execute(ctx, in)
	if err == nil {
		t.Error("expected error for overly long timezone name")
	}

	// Malformed JSON
	_, err = tool.Execute(ctx, json.RawMessage(`{not a valid json`))
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}
