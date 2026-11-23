package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // Embedded IANA timezone database for cross-platform support (especially Windows)

	"github.com/amiraq1/botlink/internal/tools"
)

type getTimeInput struct {
	Timezone string `json:"timezone"`
}

type getTimeOutput struct {
	UTC           string `json:"utc"`
	Timezone      string `json:"timezone"`
	LocalTime     string `json:"local_time"`
	Weekday       string `json:"weekday"`
	UnixTimestamp int64  `json:"unix_timestamp"`
}

// GetTimeTool implements the builtin get_time tool.
type GetTimeTool struct{}

// NewGetTimeTool returns a new instance of GetTimeTool.
func NewGetTimeTool() *GetTimeTool {
	return &GetTimeTool{}
}

func (t *GetTimeTool) Name() string {
	return "get_time"
}

func (t *GetTimeTool) Description() string {
	return "Returns current date and time in UTC and optionally in a specified IANA timezone (e.g. 'Africa/Cairo', 'America/New_York', 'UTC')."
}

func (t *GetTimeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "timezone": {
      "type": "string",
      "description": "Optional IANA timezone name, e.g. 'Africa/Cairo', 'UTC', 'America/New_York'. Defaults to UTC."
    }
  }
}`)
}

func (t *GetTimeTool) Execute(ctx context.Context, in json.RawMessage) (tools.Result, error) {
	tzName := "UTC"
	if len(in) > 0 && string(in) != "null" && string(in) != "{}" {
		var input getTimeInput
		if err := json.Unmarshal(in, &input); err != nil {
			return tools.Result{}, fmt.Errorf("invalid input JSON: %w", err)
		}
		if trimmed := strings.TrimSpace(input.Timezone); trimmed != "" {
			if len(trimmed) > 64 {
				return tools.Result{}, fmt.Errorf("timezone name exceeds maximum length of 64 characters")
			}
			tzName = trimmed
		}
	}

	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return tools.Result{}, fmt.Errorf("unknown or invalid timezone %q: %w", tzName, err)
	}

	now := time.Now()
	nowUTC := now.UTC()
	nowLoc := now.In(loc)

	out := getTimeOutput{
		UTC:           nowUTC.Format(time.RFC3339),
		Timezone:      loc.String(),
		LocalTime:     nowLoc.Format("2006-01-02 15:04:05 -0700"),
		Weekday:       nowLoc.Weekday().String(),
		UnixTimestamp: now.Unix(),
	}

	b, err := json.Marshal(out)
	if err != nil {
		return tools.Result{}, fmt.Errorf("marshal result: %w", err)
	}

	return tools.Result{Text: string(b)}, nil
}
