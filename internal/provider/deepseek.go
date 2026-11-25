// Package provider talks to the configured LLM provider. Only DeepSeek is
// supported in this version, and its origin is fixed in code: no admin-supplied
// base URL, proxy or path is accepted, so a stored connection can never redirect
// a request (or an API key) to another host.
//
// Phase 2 deliberately implements only GET /models. That call proves the key can
// reach the models endpoint; it does not prove that a chat request payload is
// valid, that the account has balance, or that generation will succeed.
package provider

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	// BaseURL is the official DeepSeek API origin. It is a constant, not
	// configuration: the store has no base_url column and handlers never read
	// an origin from a request.
	BaseURL = "https://api.deepseek.com"

	defaultTimeout      = 15 * time.Second
	maxModelsBodyBytes  = 2 << 20
	maxChatBodyBytes    = 4 << 20
	maxModels           = 500
	maxProviderMessage  = 300
	maxReplyRunes       = 20000
	userAgent           = "botlink-managed/1.0"
	redactedPlaceholder = "[redacted]"
)

// modelIDRE matches the shape of a provider model identifier. Anything with
// spaces, slashes, braces or control characters is rejected, so a model name
// can never be injected into a request path or body.
var modelIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)

var (
	// ErrTimeout means the provider did not answer within the client deadline.
	ErrTimeout = errors.New("provider request timed out")
	// ErrCanceled means the caller cancelled the request.
	ErrCanceled = errors.New("provider request canceled")
	// ErrUnreachable means no HTTP response arrived (DNS, TLS, connection).
	ErrUnreachable = errors.New("provider could not be reached")
	// ErrUnexpectedResponse means a 2xx response did not match the documented
	// shape, including redirects, malformed JSON, an empty model list and
	// oversized bodies.
	ErrUnexpectedResponse = errors.New("unexpected provider response")
	// ErrResponseTooLarge means the response body exceeded the inspection cap.
	ErrResponseTooLarge = errors.New("provider response too large")
)

// APIError is a non-2xx response from the provider. Message is sanitised and
// has the API key redacted before it is stored or returned.
type APIError struct {
	Status  int
	Code    string
	Type    string
	Message string
}

func (e *APIError) Error() string {
	msg := "deepseek API error"
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Status != 0 {
		msg += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	return msg
}

// Model is one entry of GET /models. Optional metadata is zero when the
// provider omitted it.
type Model struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	OwnedBy         string `json:"ownedBy,omitempty"`
	ContextWindow   int64  `json:"contextWindow,omitempty"`
	MaxOutputTokens int64  `json:"maxOutputTokens,omitempty"`
}

// Client is a DeepSeek API client bound to one API key and the fixed origin.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
	timeout time.Duration
}

// NewClient creates a DeepSeek client for one API key. transport is normally
// nil (a hardened default is used); tests inject a fake RoundTripper. The
// client never follows redirects and always uses the official origin.
func NewClient(apiKey string, transport http.RoundTripper) *Client {
	return newClient(BaseURL, apiKey, transport, defaultTimeout)
}

// NewClientAt creates a client for an explicit origin. It exists only for
// local integration tests with a fake provider; production always uses
// NewClient and the fixed BaseURL. Callers must gate the origin on development
// mode and keep it loopback-only.
func NewClientAt(baseURL, apiKey string, transport http.RoundTripper) *Client {
	return newClient(baseURL, apiKey, transport, defaultTimeout)
}

func newClient(baseURL, apiKey string, transport http.RoundTripper, timeout time.Duration) *Client {
	if transport == nil {
		transport = defaultTransport()
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Transport: transport,
			// A redirect could carry the Authorization header to another
			// origin. Refuse to follow any redirect; the 3xx is then reported
			// as an unexpected response.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		timeout: timeout,
	}
}

func defaultTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

type listModelsResponse struct {
	Object string `json:"object"`
	Data   []struct {
		ID              string `json:"id"`
		Object          string `json:"object"`
		OwnedBy         string `json:"owned_by"`
		Name            string `json:"name"`
		ContextWindow   *int64 `json:"context_window"`
		MaxOutputTokens *int64 `json:"max_output_tokens"`
	} `json:"data"`
}

// ListModels calls GET /models. It is a bounded probe of the model catalog, not
// a full connectivity or billing test.
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build request", ErrUnreachable)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		// Classify before returning: the raw transport error may name internal
		// hosts and must never be surfaced to an admin response or a log.
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ErrCanceled
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return nil, ErrTimeout
		}
		return nil, ErrUnreachable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("%w: provider returned a redirect, which is not followed", ErrUnexpectedResponse)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.apiError(resp)
	}

	body, err := readLimited(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed listModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w: body is not valid JSON", ErrUnexpectedResponse)
	}
	if parsed.Object != "list" {
		return nil, fmt.Errorf("%w: object is not a list", ErrUnexpectedResponse)
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("%w: the model list is empty", ErrUnexpectedResponse)
	}
	if len(parsed.Data) > maxModels {
		return nil, fmt.Errorf("%w: the model list is too long", ErrUnexpectedResponse)
	}

	out := make([]Model, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if !modelIDRE.MatchString(m.ID) {
			return nil, fmt.Errorf("%w: a model id is not a valid identifier", ErrUnexpectedResponse)
		}
		if m.Object != "model" {
			return nil, fmt.Errorf("%w: a model entry is not of type model", ErrUnexpectedResponse)
		}
		if m.ContextWindow != nil && *m.ContextWindow < 0 {
			return nil, fmt.Errorf("%w: a model has a negative context window", ErrUnexpectedResponse)
		}
		if m.MaxOutputTokens != nil && *m.MaxOutputTokens < 0 {
			return nil, fmt.Errorf("%w: a model has a negative output limit", ErrUnexpectedResponse)
		}
		model := Model{ID: m.ID, Name: cleanText(m.Name, c.apiKey, 200), OwnedBy: cleanText(m.OwnedBy, c.apiKey, 100)}
		if m.ContextWindow != nil {
			model.ContextWindow = *m.ContextWindow
		}
		if m.MaxOutputTokens != nil {
			model.MaxOutputTokens = *m.MaxOutputTokens
		}
		out = append(out, model)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Chat completions
// ---------------------------------------------------------------------------

// ChatMessage is one message of a chat-completions request. Only plain text
// content is supported: this runner never sends images, tools or tool output.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatUsage is the usage block the provider reports for a completion. The
// documented schema marks these fields required, but a response that omits
// them is still accepted with HasUsage=false so the caller records an unknown
// cost instead of guessing.
type ChatUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// ChatResult is the assistant reply of one non-streaming completion. It never
// carries reasoning_content, tool calls or raw provider JSON.
type ChatResult struct {
	Content      string
	FinishReason string
	Usage        ChatUsage
	HasUsage     bool
}

type chatCompletionRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	MaxTokens int64         `json:"max_tokens,omitempty"`
	Stream    bool          `json:"stream"`
	// Thinking is always disabled: DeepSeek enables thinking when the field
	// is omitted, which would spend tokens on hidden reasoning we never use.
	Thinking struct {
		Type string `json:"type"`
	} `json:"thinking"`
	// No tools, tool_choice or functions are ever set. The runner must not be
	// able to trigger an external action in this phase.
}

type chatCompletionResponse struct {
	Object  string `json:"object"`
	Choices []struct {
		Message struct {
			Role             string            `json:"role"`
			Content          *string           `json:"content"`
			ReasoningContent *string           `json:"reasoning_content"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
		TotalTokens      *int64 `json:"total_tokens"`
	} `json:"usage"`
}

// ChatCompletion calls POST /chat/completions on the fixed official origin
// with a non-streaming request. model and messages are validated before any
// request is built; the API key is only ever placed in the Authorization
// header. A 2xx response is accepted only when it carries one assistant text
// message and no tool call. Redirects are refused by the client.
func (c *Client) ChatCompletion(ctx context.Context, model string, messages []ChatMessage, maxTokens int64) (*ChatResult, error) {
	if !modelIDRE.MatchString(model) {
		return nil, fmt.Errorf("%w: invalid model name", ErrUnexpectedResponse)
	}
	if len(messages) == 0 || len(messages) > 200 {
		return nil, fmt.Errorf("%w: message list length is out of range", ErrUnexpectedResponse)
	}
	if maxTokens < 1 || maxTokens > 393216 {
		return nil, fmt.Errorf("%w: max_tokens is out of range", ErrUnexpectedResponse)
	}
	for _, m := range messages {
		switch m.Role {
		case "system", "user", "assistant":
		default:
			return nil, fmt.Errorf("%w: unsupported message role", ErrUnexpectedResponse)
		}
		if strings.TrimSpace(m.Content) == "" {
			return nil, fmt.Errorf("%w: empty message content", ErrUnexpectedResponse)
		}
	}

	body := chatCompletionRequest{
		Model:     model,
		Messages:  messages,
		MaxTokens: maxTokens,
		Stream:    false,
	}
	body.Thinking.Type = "disabled"
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%w: encode request", ErrUnexpectedResponse)
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: build request", ErrUnreachable)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ErrCanceled
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return nil, ErrTimeout
		}
		return nil, ErrUnreachable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("%w: provider returned a redirect, which is not followed", ErrUnexpectedResponse)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.apiError(resp)
	}
	respBody, err := readLimitedSize(resp.Body, maxChatBodyBytes)
	if err != nil {
		return nil, err
	}

	var parsed chatCompletionResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("%w: body is not valid JSON", ErrUnexpectedResponse)
	}
	if parsed.Object != "chat.completion" {
		return nil, fmt.Errorf("%w: object is not a chat completion", ErrUnexpectedResponse)
	}
	if len(parsed.Choices) != 1 {
		return nil, fmt.Errorf("%w: expected exactly one choice", ErrUnexpectedResponse)
	}
	choice := parsed.Choices[0]
	if choice.Message.Role != "assistant" {
		return nil, fmt.Errorf("%w: the choice is not an assistant message", ErrUnexpectedResponse)
	}
	if len(choice.Message.ToolCalls) > 0 {
		return nil, fmt.Errorf("%w: the model returned a tool call, which is not allowed", ErrUnexpectedResponse)
	}
	if choice.Message.Content == nil {
		return nil, fmt.Errorf("%w: the assistant message has no text content", ErrUnexpectedResponse)
	}
	finish := ""
	if choice.FinishReason != nil {
		finish = *choice.FinishReason
	}
	switch finish {
	case "stop", "length":
	case "content_filter", "insufficient_system_resource", "aborted", "tool_calls":
		return nil, fmt.Errorf("%w: the generation ended with finish_reason=%s", ErrUnexpectedResponse, finish)
	default:
		return nil, fmt.Errorf("%w: unknown finish_reason", ErrUnexpectedResponse)
	}

	result := &ChatResult{
		Content:      cleanReply(*choice.Message.Content, c.apiKey, maxReplyRunes),
		FinishReason: finish,
	}
	if parsed.Usage != nil {
		result.HasUsage = true
		if parsed.Usage.PromptTokens != nil {
			result.Usage.PromptTokens = *parsed.Usage.PromptTokens
		}
		if parsed.Usage.CompletionTokens != nil {
			result.Usage.CompletionTokens = *parsed.Usage.CompletionTokens
		}
		if parsed.Usage.TotalTokens != nil {
			result.Usage.TotalTokens = *parsed.Usage.TotalTokens
		}
		if result.Usage.PromptTokens < 0 || result.Usage.CompletionTokens < 0 || result.Usage.TotalTokens < 0 {
			return nil, fmt.Errorf("%w: usage contains a negative token count", ErrUnexpectedResponse)
		}
	}
	return result, nil
}

func readLimitedSize(r io.Reader, max int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, ErrUnexpectedResponse
	}
	if int64(len(body)) > max {
		return nil, ErrResponseTooLarge
	}
	return body, nil
}

// cleanReply preserves line breaks and tabs (unlike cleanText, which is for
// single-line metadata), strips control and bidi characters, redacts the API
// key and caps the rune count.
func cleanReply(s, key string, max int) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, redactedPlaceholder)
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if isInvisible(r) {
			continue
		}
		if r == '\r' {
			continue
		}
		if r < 32 && r != '\n' && r != '\t' {
			continue
		}
		if r == 127 {
			continue
		}
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// ErrorCode maps a client error to a stable, short code stored on a run. It
// never contains provider text, request material or the API key.
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrTimeout):
		return "provider_timeout"
	case errors.Is(err, ErrCanceled):
		return "provider_canceled"
	case errors.Is(err, ErrUnreachable):
		return "provider_unreachable"
	case errors.Is(err, ErrResponseTooLarge):
		return "provider_response_too_large"
	case errors.Is(err, ErrUnexpectedResponse):
		return "provider_unexpected_response"
	}
	var api *APIError
	if errors.As(err, &api) {
		switch {
		case api.Status == 401 || api.Status == 403:
			return "provider_auth"
		case api.Status == 402:
			return "provider_insufficient_balance"
		case api.Status == 429:
			return "provider_rate_limited"
		case api.Status == 400 || api.Status == 404 || api.Status == 422:
			return "provider_rejected"
		case api.Status >= 500:
			return "provider_unavailable"
		default:
			return "provider_error"
		}
	}
	return "provider_error"
}

func readLimited(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxModelsBodyBytes+1))
	if err != nil {
		return nil, ErrUnexpectedResponse
	}
	if len(body) > maxModelsBodyBytes {
		return nil, ErrResponseTooLarge
	}
	return body, nil
}

// apiError decodes the documented {"error":{...}} envelope when present and
// falls back to the status text. The body is capped, never echoed raw, and the
// API key is redacted in case the provider echoes request material.
func (c *Client) apiError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))

	var env struct {
		Error struct {
			Message string          `json:"message"`
			Type    json.RawMessage `json:"type"`
			Code    json.RawMessage `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)

	msg := cleanText(env.Error.Message, c.apiKey, maxProviderMessage)
	if msg == "" {
		msg = cleanText(http.StatusText(resp.StatusCode), c.apiKey, maxProviderMessage)
	}
	return &APIError{
		Status:  resp.StatusCode,
		Code:    rawField(env.Error.Code, c.apiKey),
		Type:    rawField(env.Error.Type, c.apiKey),
		Message: msg,
	}
}

// rawField turns a JSON scalar into display text. Unknown shapes become empty
// instead of failing the response, because the status code is the real signal.
func rawField(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return cleanText(s, key, 100)
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return cleanText(n.String(), key, 100)
	}
	return ""
}

// ErrorMessage maps a client error to a short, safe admin-facing reason. It
// never includes the API key, request headers or raw transport details.
func ErrorMessage(err error) string {
	switch {
	case errors.Is(err, ErrTimeout):
		return "the provider did not answer within the time limit"
	case errors.Is(err, ErrCanceled):
		return "the check was cancelled"
	case errors.Is(err, ErrUnreachable):
		return "the provider could not be reached"
	case errors.Is(err, ErrResponseTooLarge):
		return "the provider response was too large to inspect"
	case errors.Is(err, ErrUnexpectedResponse):
		return "the provider returned an unexpected response"
	}
	var api *APIError
	if errors.As(err, &api) {
		msg := api.Message
		if msg == "" {
			msg = http.StatusText(api.Status)
		}
		return fmt.Sprintf("the provider rejected the request (HTTP %d): %s", api.Status, msg)
	}
	return "the provider check failed"
}

// isInvisible reports bidi overrides, zero-width and BOM characters that could
// visually reorder a model name in the admin UI.
func isInvisible(r rune) bool {
	switch {
	case r == 0x061C || r == 0xFEFF:
		return true
	case r >= 0x200B && r <= 0x200F:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// cleanText removes control characters, collapses whitespace, redacts the API
// key and caps the rune length.
func cleanText(s, key string, max int) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, redactedPlaceholder)
	}
	var b strings.Builder
	n := 0
	space := false
	for _, r := range s {
		if isInvisible(r) {
			continue
		}
		if r < 32 || r == 127 {
			r = ' '
		}
		if r == ' ' {
			if space || b.Len() == 0 {
				continue
			}
			space = true
		} else {
			space = false
		}
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}
