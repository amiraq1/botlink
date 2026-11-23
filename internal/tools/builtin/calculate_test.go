package builtin_test

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/amiraq1/botlink/internal/tools/builtin"
)

func TestCalculateTool_BasicArithmetic(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"2 + 2", 4},
		{"10 - 4", 6},
		{"3 * 7", 21},
		{"20 / 4", 5},
		{"10 % 3", 1},
		{"2 + 3 * 4", 14},
		{"(2 + 3) * 4", 20},
		{"10 - 2 - 3", 5},
		{"100 / 10 / 2", 5},
		{"2.5 + 3.5", 6},
		{"1.5 * 2", 3},
		{"1e3 + 25", 1025},
		{"2.5e-1", 0.25},
	}

	for _, tc := range cases {
		got, err := builtin.Evaluate(tc.expr)
		if err != nil {
			t.Errorf("expr %q failed: %v", tc.expr, err)
			continue
		}
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("expr %q = %g; want %g", tc.expr, got, tc.want)
		}
	}
}

func TestCalculateTool_UnaryAndPrecedence(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"-5 + 3", -2},
		{"+4", 4},
		{"-(2 * 3)", -6},
		{"- -5", 5},
		{"3 * -2", -6},
		{"-3 * -4", 12},
		{"-(10 - 4)", -6},
	}

	for _, tc := range cases {
		got, err := builtin.Evaluate(tc.expr)
		if err != nil {
			t.Errorf("expr %q failed: %v", tc.expr, err)
			continue
		}
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("expr %q = %g; want %g", tc.expr, got, tc.want)
		}
	}
}

func TestCalculateTool_Power(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"2 ^ 3", 8},
		{"2 ** 3", 8},
		{"3 ^ 2 + 1", 10},
		{"2 ^ 3 ^ 2", 512}, // right-associative: 2^(3^2) = 2^9 = 512
		{"(2 ^ 3) ^ 2", 64},
		{"4 ^ 0.5", 2},
		{"2 ^ -1", 0.5},
	}

	for _, tc := range cases {
		got, err := builtin.Evaluate(tc.expr)
		if err != nil {
			t.Errorf("expr %q failed: %v", tc.expr, err)
			continue
		}
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("expr %q = %g; want %g", tc.expr, got, tc.want)
		}
	}
}

func TestCalculateTool_ExecuteJSON(t *testing.T) {
	tool := builtin.NewCalculateTool()
	ctx := context.Background()

	in, _ := json.Marshal(map[string]string{"expression": "2 * (10 + 5)"})
	res, err := tool.Execute(ctx, in)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
		t.Fatalf("Unmarshal failed: %v, text=%s", err, res.Text)
	}

	if out["value"].(float64) != 30 {
		t.Errorf("expected value 30, got %v", out["value"])
	}
	if out["formatted"] != "30" {
		t.Errorf("expected formatted '30', got %v", out["formatted"])
	}
}

func TestCalculateTool_SafetyAndEdgeCases(t *testing.T) {
	errorCases := []struct {
		name string
		expr string
	}{
		{"division by zero", "10 / 0"},
		{"division by tiny zero", "10 / 0.0000000000000000001"},
		{"modulo by zero", "10 % 0"},
		{"empty expression", ""},
		{"whitespace only", "   "},
		{"unmatched opening paren", "(2 + 3"},
		{"unmatched closing paren", "2 + 3)"},
		{"missing operator", "2 3"},
		{"trailing operator", "2 +"},
		{"invalid character letters", "2 + abc"},
		{"command injection attempt", "system('calc')"},
		{"alert call", "alert(1)"},
		{"dollar sign", "$100 + 2"},
		{"backticks", "`ls`"},
		{"complex root of negative", "(-4) ^ 0.5"},
		{"huge exponent", "2 ^ 1001"},
		{"huge base and exponent", "1e16 ^ 3"},
	}

	for _, tc := range errorCases {
		_, err := builtin.Evaluate(tc.expr)
		if err == nil {
			t.Errorf("[%s] expected error for %q, but got nil", tc.name, tc.expr)
		}
	}
}

func TestCalculateTool_MaxLimits(t *testing.T) {
	tool := builtin.NewCalculateTool()
	ctx := context.Background()

	// 1. Length cap (> 256 chars)
	longExpr := "1" + strings.Repeat(" + 1", 100) // ~400 chars
	in, _ := json.Marshal(map[string]string{"expression": longExpr})
	_, err := tool.Execute(ctx, in)
	if err == nil {
		t.Error("expected error for expression > 256 characters")
	}

	// 2. Parentheses depth > 16
	deepExpr := strings.Repeat("(", 17) + "1" + strings.Repeat(")", 17)
	_, err = builtin.Evaluate(deepExpr)
	if err == nil {
		t.Error("expected error for paren depth > 16")
	}

	// 3. Parentheses depth exactly 16 (should pass)
	validDeepExpr := strings.Repeat("(", 16) + "1" + strings.Repeat(")", 16)
	val, err := builtin.Evaluate(validDeepExpr)
	if err != nil {
		t.Errorf("depth 16 failed: %v", err)
	}
	if val != 1 {
		t.Errorf("depth 16 want 1, got %g", val)
	}
}
