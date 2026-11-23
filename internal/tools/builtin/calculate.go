package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/amiraq1/botlink/internal/tools"
)

const (
	maxExpressionLength = 256
	maxParenDepth       = 16
	maxExponent         = 1000.0
)

type calculateInput struct {
	Expression string `json:"expression"`
}

type calculateOutput struct {
	Expression string  `json:"expression"`
	Value      float64 `json:"value"`
	Formatted  string  `json:"formatted"`
}

// CalculateTool implements a pure mathematical expression evaluator.
type CalculateTool struct{}

// NewCalculateTool creates a new instance of CalculateTool.
func NewCalculateTool() *CalculateTool {
	return &CalculateTool{}
}

func (t *CalculateTool) Name() string {
	return "calculate"
}

func (t *CalculateTool) Description() string {
	return "Safely evaluates mathematical expressions supporting +, -, *, /, ^ (or **), % and parentheses. Pure mathematical parser with safety caps on depth and exponent size."
}

func (t *CalculateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["expression"],
  "properties": {
    "expression": {
      "type": "string",
      "description": "Mathematical expression to evaluate, e.g. '2 + 2 * (3 ^ 2)'"
    }
  }
}`)
}

func (t *CalculateTool) Execute(ctx context.Context, in json.RawMessage) (tools.Result, error) {
	var input calculateInput
	if err := json.Unmarshal(in, &input); err != nil {
		return tools.Result{}, fmt.Errorf("invalid input JSON: %w", err)
	}

	expr := strings.TrimSpace(input.Expression)
	if expr == "" {
		return tools.Result{}, errors.New("expression cannot be empty")
	}
	if len([]rune(expr)) > maxExpressionLength {
		return tools.Result{}, fmt.Errorf("expression exceeds maximum length of %d characters", maxExpressionLength)
	}

	val, err := Evaluate(expr)
	if err != nil {
		return tools.Result{}, err
	}

	formatted := formatFloat(val)
	out := calculateOutput{
		Expression: expr,
		Value:      val,
		Formatted:  formatted,
	}

	b, err := json.Marshal(out)
	if err != nil {
		return tools.Result{}, fmt.Errorf("marshal result: %w", err)
	}

	return tools.Result{Text: string(b)}, nil
}

func formatFloat(v float64) string {
	if isTrunc(v) && math.Abs(v) < 1e15 {
		return fmt.Sprintf("%.0f", v)
	}
	return strconv.FormatFloat(v, 'g', 14, 64)
}

func isTrunc(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Trunc(v) == v
}

// ---------------------------------------------------------------------------
// Lexer & Parser
// ---------------------------------------------------------------------------

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokNumber
	tokPlus
	tokMinus
	tokMul
	tokDiv
	tokMod
	tokPow
	tokLParen
	tokRParen
)

type token struct {
	kind tokenKind
	val  float64
	text string
	pos  int
}

type lexer struct {
	input []rune
	pos   int
}

func newLexer(s string) *lexer {
	return &lexer{input: []rune(s), pos: 0}
}

func (l *lexer) nextToken() (token, error) {
	for l.pos < len(l.input) && unicode.IsSpace(l.input[l.pos]) {
		l.pos++
	}

	if l.pos >= len(l.input) {
		return token{kind: tokEOF, pos: l.pos}, nil
	}

	ch := l.input[l.pos]
	pos := l.pos

	switch ch {
	case '+':
		l.pos++
		return token{kind: tokPlus, text: "+", pos: pos}, nil
	case '-':
		l.pos++
		return token{kind: tokMinus, text: "-", pos: pos}, nil
	case '*':
		l.pos++
		if l.pos < len(l.input) && l.input[l.pos] == '*' {
			l.pos++
			return token{kind: tokPow, text: "**", pos: pos}, nil
		}
		return token{kind: tokMul, text: "*", pos: pos}, nil
	case '/':
		l.pos++
		return token{kind: tokDiv, text: "/", pos: pos}, nil
	case '%':
		l.pos++
		return token{kind: tokMod, text: "%", pos: pos}, nil
	case '^':
		l.pos++
		return token{kind: tokPow, text: "^", pos: pos}, nil
	case '(':
		l.pos++
		return token{kind: tokLParen, text: "(", pos: pos}, nil
	case ')':
		l.pos++
		return token{kind: tokRParen, text: ")", pos: pos}, nil
	}

	if unicode.IsDigit(ch) || ch == '.' {
		start := l.pos
		hasDot := false
		for l.pos < len(l.input) {
			c := l.input[l.pos]
			if c == '.' {
				if hasDot {
					return token{}, fmt.Errorf("invalid number at position %d: multiple decimal points", l.pos)
				}
				hasDot = true
				l.pos++
			} else if unicode.IsDigit(c) {
				l.pos++
			} else {
				break
			}
		}

		// Scientific notation: e.g. 1e5, 2.5e-3
		if l.pos < len(l.input) && (l.input[l.pos] == 'e' || l.input[l.pos] == 'E') {
			l.pos++
			if l.pos < len(l.input) && (l.input[l.pos] == '+' || l.input[l.pos] == '-') {
				l.pos++
			}
			expDigits := 0
			for l.pos < len(l.input) && unicode.IsDigit(l.input[l.pos]) {
				l.pos++
				expDigits++
			}
			if expDigits == 0 {
				return token{}, fmt.Errorf("invalid scientific notation at position %d: missing exponent digits", start)
			}
		}

		numStr := string(l.input[start:l.pos])
		v, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return token{}, fmt.Errorf("invalid number %q at position %d: %w", numStr, start, err)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return token{}, fmt.Errorf("number %q at position %d out of float64 range", numStr, start)
		}
		return token{kind: tokNumber, val: v, text: numStr, pos: start}, nil
	}

	return token{}, fmt.Errorf("invalid character %q at position %d", string(ch), pos)
}

// ---------------------------------------------------------------------------
// Recursive Descent Parser
// ---------------------------------------------------------------------------

type parser struct {
	tokens []token
	idx    int
	depth  int
}

// Evaluate parses and computes the mathematical result of an expression string.
func Evaluate(expr string) (float64, error) {
	lex := newLexer(expr)
	var tokens []token
	for {
		tok, err := lex.nextToken()
		if err != nil {
			return 0, err
		}
		tokens = append(tokens, tok)
		if tok.kind == tokEOF {
			break
		}
	}

	if len(tokens) <= 1 {
		return 0, errors.New("empty expression")
	}

	p := &parser{tokens: tokens, idx: 0, depth: 0}
	val, err := p.parseAddSub()
	if err != nil {
		return 0, err
	}

	if p.current().kind != tokEOF {
		return 0, fmt.Errorf("unexpected token %q at position %d", p.current().text, p.current().pos)
	}

	if math.IsNaN(val) || math.IsInf(val, 0) {
		return 0, errors.New("arithmetic overflow or undefined result")
	}

	return val, nil
}

func (p *parser) current() token {
	if p.idx < len(p.tokens) {
		return p.tokens[p.idx]
	}
	return token{kind: tokEOF}
}

func (p *parser) consume() token {
	tok := p.current()
	if p.idx < len(p.tokens) {
		p.idx++
	}
	return tok
}

// parseAddSub parses + and - (binary)
func (p *parser) parseAddSub() (float64, error) {
	left, err := p.parseMulDiv()
	if err != nil {
		return 0, err
	}

	for {
		tok := p.current()
		if tok.kind == tokPlus {
			p.consume()
			right, err := p.parseMulDiv()
			if err != nil {
				return 0, err
			}
			left = left + right
			if err := checkFinite(left); err != nil {
				return 0, err
			}
		} else if tok.kind == tokMinus {
			p.consume()
			right, err := p.parseMulDiv()
			if err != nil {
				return 0, err
			}
			left = left - right
			if err := checkFinite(left); err != nil {
				return 0, err
			}
		} else {
			break
		}
	}
	return left, nil
}

// parseMulDiv parses *, /, %
func (p *parser) parseMulDiv() (float64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}

	for {
		tok := p.current()
		if tok.kind == tokMul {
			p.consume()
			right, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			left = left * right
			if err := checkFinite(left); err != nil {
				return 0, err
			}
		} else if tok.kind == tokDiv {
			p.consume()
			right, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			if right == 0.0 || math.Abs(right) < 1e-15 {
				return 0, errors.New("division by zero")
			}
			left = left / right
			if err := checkFinite(left); err != nil {
				return 0, err
			}
		} else if tok.kind == tokMod {
			p.consume()
			right, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			if right == 0.0 || math.Abs(right) < 1e-15 {
				return 0, errors.New("modulo by zero")
			}
			left = math.Mod(left, right)
			if err := checkFinite(left); err != nil {
				return 0, err
			}
		} else {
			break
		}
	}
	return left, nil
}

// parseUnary parses unary + and -
func (p *parser) parseUnary() (float64, error) {
	tok := p.current()
	if tok.kind == tokPlus {
		p.consume()
		return p.parseUnary()
	}
	if tok.kind == tokMinus {
		p.consume()
		val, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		return -val, checkFinite(-val)
	}
	return p.parsePower()
}

// parsePower parses ^ or ** (right-associative: 2^3^2 = 2^(3^2) = 512)
func (p *parser) parsePower() (float64, error) {
	base, err := p.parsePrimary()
	if err != nil {
		return 0, err
	}

	if p.current().kind == tokPow {
		p.consume()
		// Recursively parse power for right-associativity
		exp, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		return safePow(base, exp)
	}
	return base, nil
}

// parsePrimary parses numbers and parenthesized expressions
func (p *parser) parsePrimary() (float64, error) {
	tok := p.current()

	if tok.kind == tokNumber {
		p.consume()
		return tok.val, nil
	}

	if tok.kind == tokLParen {
		p.depth++
		if p.depth > maxParenDepth {
			return 0, fmt.Errorf("parentheses nesting exceeds maximum depth of %d", maxParenDepth)
		}
		p.consume()

		val, err := p.parseAddSub()
		if err != nil {
			return 0, err
		}

		if p.current().kind != tokRParen {
			return 0, fmt.Errorf("missing closing parenthesis ')' at position %d", p.current().pos)
		}
		p.consume()
		p.depth--
		return val, nil
	}

	if tok.kind == tokEOF {
		return 0, errors.New("unexpected end of expression")
	}

	return 0, fmt.Errorf("unexpected token %q at position %d", tok.text, tok.pos)
}

func safePow(base, exp float64) (float64, error) {
	if math.Abs(exp) > maxExponent {
		return 0, fmt.Errorf("exponent %g exceeds maximum magnitude of %g", exp, maxExponent)
	}

	if math.Abs(base) > 1e15 && exp > 2 {
		return 0, errors.New("base and exponent too large (potential overflow)")
	}

	// Negative base with non-integer exponent yields complex numbers
	if base < 0 && !isTrunc(exp) {
		return 0, errors.New("complex numbers not supported (negative base with fractional exponent)")
	}

	res := math.Pow(base, exp)
	if err := checkFinite(res); err != nil {
		return 0, err
	}
	return res, nil
}

func checkFinite(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return errors.New("arithmetic overflow or undefined result")
	}
	return nil
}
