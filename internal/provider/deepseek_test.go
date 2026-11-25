package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestChatCompletionRequestShapeAndResponse(t *testing.T) {
	var got *http.Request
	var body map[string]any
	client := newClient(BaseURL, testKey, rtFunc(func(r *http.Request) (*http.Response, error) {
		got = r
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		return fakeResponse(http.StatusOK, `{
  "id":"x","object":"chat.completion","created":1,"model":"deepseek-flash",
  "choices":[{"index":0,"message":{"role":"assistant","content":"مرحبا\nبك"},"finish_reason":"stop"}],
  "usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19}
}`), nil
	}), time.Second)

	res, err := client.ChatCompletion(context.Background(), "deepseek-flash", []ChatMessage{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "hello"},
	}, 512)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got.Method != http.MethodPost || got.URL.String() != "https://api.deepseek.com/chat/completions" {
		t.Errorf("request = %s %s; want POST on the fixed official origin", got.Method, got.URL)
	}
	if auth := got.Header.Get("Authorization"); auth != "Bearer "+testKey {
		t.Errorf("Authorization = %q", auth)
	}
	if got.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", got.Header.Get("Content-Type"))
	}
	if body["model"] != "deepseek-flash" || body["stream"] != false {
		t.Errorf("model/stream = %v/%v", body["model"], body["stream"])
	}
	if max, _ := body["max_tokens"].(float64); max != 512 {
		t.Errorf("max_tokens = %v; want 512", body["max_tokens"])
	}
	thinking, _ := body["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Errorf("thinking = %v; want explicitly disabled", body["thinking"])
	}
	for _, forbidden := range []string{"tools", "tool_choice", "functions"} {
		if _, ok := body[forbidden]; ok {
			t.Errorf("request must not include %q", forbidden)
		}
	}
	if res.Content != "مرحبا\nبك" || res.FinishReason != "stop" {
		t.Errorf("result = %+v", res)
	}
	if !res.HasUsage || res.Usage.PromptTokens != 12 || res.Usage.CompletionTokens != 7 || res.Usage.TotalTokens != 19 {
		t.Errorf("usage = %+v hasUsage=%v", res.Usage, res.HasUsage)
	}
}

func TestChatCompletionRejectsUnexpectedResponses(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"not json", `not-json`},
		{"wrong object", `{"object":"list","choices":[]}`},
		{"no choices", `{"object":"chat.completion","choices":[]}`},
		{"nil content", `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"stop"}]}`},
		{"tool call", `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"c"}]},"finish_reason":"tool_calls"}]}`},
		{"content filter", `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"content_filter"}]}`},
		{"wrong role", `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"user","content":"x"},"finish_reason":"stop"}]}`},
		{"two choices", `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"a"},"finish_reason":"stop"},{"index":1,"message":{"role":"assistant","content":"b"},"finish_reason":"stop"}]}`},
		{"negative usage", `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"a"},"finish_reason":"stop"}],"usage":{"prompt_tokens":-1}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(BaseURL, testKey, rtFunc(func(r *http.Request) (*http.Response, error) {
				return fakeResponse(http.StatusOK, tc.body), nil
			}), time.Second)
			_, err := client.ChatCompletion(context.Background(), "deepseek-flash",
				[]ChatMessage{{Role: "user", Content: "hi"}}, 16)
			if !errors.Is(err, ErrUnexpectedResponse) {
				t.Fatalf("error = %v; want ErrUnexpectedResponse", err)
			}
		})
	}
}

func TestChatCompletionRejectsBadInputBeforeSending(t *testing.T) {
	calls := 0
	client := newClient(BaseURL, testKey, rtFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return fakeResponse(http.StatusOK, `{}`), nil
	}), time.Second)
	cases := []struct {
		name     string
		model    string
		messages []ChatMessage
		max      int64
	}{
		{"model injection", "bad model/../x", []ChatMessage{{Role: "user", Content: "hi"}}, 16},
		{"empty messages", "deepseek-flash", nil, 16},
		{"empty content", "deepseek-flash", []ChatMessage{{Role: "user", Content: "  "}}, 16},
		{"tool role", "deepseek-flash", []ChatMessage{{Role: "tool", Content: "hi"}}, 16},
		{"zero max tokens", "deepseek-flash", []ChatMessage{{Role: "user", Content: "hi"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.ChatCompletion(context.Background(), tc.model, tc.messages, tc.max); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if calls != 0 {
		t.Fatalf("transport received %d request(s) for invalid input; want 0", calls)
	}
}

func TestChatCompletionAPIErrorCodes(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusUnauthorized, "provider_auth"},
		{http.StatusForbidden, "provider_auth"},
		{http.StatusPaymentRequired, "provider_insufficient_balance"},
		{http.StatusTooManyRequests, "provider_rate_limited"},
		{http.StatusBadRequest, "provider_rejected"},
		{http.StatusServiceUnavailable, "provider_unavailable"},
	}
	for _, tc := range cases {
		client := newClient(BaseURL, testKey, rtFunc(func(r *http.Request) (*http.Response, error) {
			return fakeResponse(tc.status, `{"error":{"message":"denied"}}`), nil
		}), time.Second)
		_, err := client.ChatCompletion(context.Background(), "deepseek-flash",
			[]ChatMessage{{Role: "user", Content: "hi"}}, 16)
		if got := ErrorCode(err); got != tc.code {
			t.Errorf("status %d: code = %q; want %q (err=%v)", tc.status, got, tc.code, err)
		}
		if strings.Contains(ErrorMessage(err), testKey) || strings.Contains(err.Error(), testKey) {
			t.Errorf("status %d leaks the key", tc.status)
		}
	}
}

func TestChatCompletionTimeoutAndCancel(t *testing.T) {
	block := rtFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	client := newClient(BaseURL, testKey, block, 25*time.Millisecond)
	start := time.Now()
	_, err := client.ChatCompletion(context.Background(), "deepseek-flash",
		[]ChatMessage{{Role: "user", Content: "hi"}}, 16)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout error = %v; want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client = NewClient(testKey, rtFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	if _, err := client.ChatCompletion(ctx, "deepseek-flash",
		[]ChatMessage{{Role: "user", Content: "hi"}}, 16); !errors.Is(err, ErrCanceled) {
		t.Fatalf("cancel error = %v; want ErrCanceled", err)
	}
}

func TestChatCompletionRedactsKeyFromContentAndErrors(t *testing.T) {
	client := newClient(BaseURL, testKey, rtFunc(func(r *http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusOK, `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"leak `+testKey+` here"},"finish_reason":"stop"}]}`), nil
	}), time.Second)
	res, err := client.ChatCompletion(context.Background(), "deepseek-flash",
		[]ChatMessage{{Role: "user", Content: "hi"}}, 16)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if strings.Contains(res.Content, testKey) || !strings.Contains(res.Content, redactedPlaceholder) {
		t.Fatalf("content was not redacted: %q", res.Content)
	}
}

const testKey = "sk-test-0123456789abcdefWXYZ"

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

const validModelsBody = `{
  "object": "list",
  "data": [
    {"id": "deepseek-flash", "object": "model", "owned_by": "deepseek",
     "name": "DeepSeek-V4.1-Flash", "context_window": 1048576, "max_output_tokens": 393216},
    {"id": "deepseek-v4-pro", "object": "model", "owned_by": "deepseek", "name": "DeepSeek-V4-Pro"}
  ]
}`

func TestListModelsUsesFixedOriginAndBearerKey(t *testing.T) {
	var got *http.Request
	client := newClient(BaseURL, testKey, rtFunc(func(r *http.Request) (*http.Response, error) {
		got = r
		return fakeResponse(http.StatusOK, validModelsBody), nil
	}), time.Second)

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got == nil {
		t.Fatal("no request reached the transport")
	}
	if got.Method != http.MethodGet {
		t.Errorf("method = %s; want GET", got.Method)
	}
	if got.URL.String() != "https://api.deepseek.com/models" {
		t.Errorf("url = %s; want the fixed official models URL", got.URL)
	}
	if got.Host != "api.deepseek.com" {
		t.Errorf("host = %q; want api.deepseek.com", got.Host)
	}
	if auth := got.Header.Get("Authorization"); auth != "Bearer "+testKey {
		t.Errorf("Authorization = %q; want a Bearer header with the decrypted key", auth)
	}
	if got.Header.Get("Accept") != "application/json" {
		t.Errorf("Accept = %q", got.Header.Get("Accept"))
	}
	if got.Header.Get("User-Agent") == "" {
		t.Error("User-Agent is empty")
	}
	if strings.Contains(got.URL.String(), "?") {
		t.Errorf("unexpected query string on models request: %s", got.URL)
	}

	if len(models) != 2 {
		t.Fatalf("len(models) = %d; want 2", len(models))
	}
	if models[0].ID != "deepseek-flash" || models[0].Name != "DeepSeek-V4.1-Flash" ||
		models[0].OwnedBy != "deepseek" || models[0].ContextWindow != 1048576 ||
		models[0].MaxOutputTokens != 393216 {
		t.Errorf("model[0] = %+v", models[0])
	}
	if models[1].ID != "deepseek-v4-pro" || models[1].ContextWindow != 0 {
		t.Errorf("model[1] = %+v", models[1])
	}
}

func TestListModelsTimeout(t *testing.T) {
	block := rtFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	client := newClient(BaseURL, testKey, block, 25*time.Millisecond)

	start := time.Now()
	_, err := client.ListModels(context.Background())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v; want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %v; the context deadline was not enforced", elapsed)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("timeout error leaks the API key: %v", err)
	}
}

func TestListModelsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient(testKey, rtFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))

	_, err := client.ListModels(ctx)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("error = %v; want ErrCanceled", err)
	}
}

func TestListModelsAPIErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantInMsg  string
	}{
		{"unauthorized", 401, `{"error":{"message":"Authentication Fails (no such user)","type":"authentication_error","code":"invalid_api_key"}}`, 401, "Authentication Fails"},
		{"insufficient balance", 402, `{"error":{"message":"Insufficient Balance","type":null,"code":402}}`, 402, "Insufficient Balance"},
		{"rate limited", 429, `{"error":{"message":"Rate Limit Reached"}}`, 429, "Rate Limit Reached"},
		{"html body", 502, `<html>bad gateway</html>`, 502, "Bad Gateway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient(testKey, rtFunc(func(*http.Request) (*http.Response, error) {
				return fakeResponse(tc.status, tc.body), nil
			}))

			_, err := client.ListModels(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v (%T); want *APIError", err, err)
			}
			if apiErr.Status != tc.wantStatus {
				t.Errorf("status = %d; want %d", apiErr.Status, tc.wantStatus)
			}
			if !strings.Contains(apiErr.Message, tc.wantInMsg) {
				t.Errorf("message = %q; want it to contain %q", apiErr.Message, tc.wantInMsg)
			}
			msg := ErrorMessage(err)
			if !strings.Contains(msg, "HTTP "+itoa(tc.wantStatus)) {
				t.Errorf("admin message = %q; want the HTTP status", msg)
			}
			if strings.Contains(err.Error(), testKey) || strings.Contains(msg, testKey) {
				t.Errorf("provider error leaks the API key: %v / %q", err, msg)
			}
		})
	}
}

func TestProviderEchoedKeyIsRedacted(t *testing.T) {
	client := NewClient(testKey, rtFunc(func(*http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusUnauthorized,
			`{"error":{"message":"Incorrect API key provided: `+testKey+` — check your key","code":"invalid_api_key"}}`), nil
	}))

	_, err := client.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error leaks the API key: %v", err)
	}
	msg := ErrorMessage(err)
	if strings.Contains(msg, testKey) {
		t.Fatalf("admin message leaks the API key: %q", msg)
	}
	if !strings.Contains(msg, "[redacted]") {
		t.Errorf("admin message = %q; want the echoed key replaced with [redacted]", msg)
	}
}

func TestTransportFailureDoesNotLeakDetails(t *testing.T) {
	client := NewClient(testKey, rtFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 10.1.2.3:443 failed for " + testKey)
	}))

	_, err := client.ListModels(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("error = %v; want ErrUnreachable", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "10.1.2.3") {
		t.Fatalf("transport error leaks details: %v", err)
	}
	if msg := ErrorMessage(err); strings.Contains(msg, testKey) {
		t.Fatalf("admin message leaks the API key: %q", msg)
	}
}

func TestListModelsUnexpectedResponses(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		want  error
		check func(t *testing.T, err error)
	}{
		{name: "invalid json", body: `{"object":"list",`, want: ErrUnexpectedResponse},
		{name: "not a list", body: `{"object":"collection","data":[]}`, want: ErrUnexpectedResponse},
		{name: "empty model list", body: `{"object":"list","data":[]}`, want: ErrUnexpectedResponse},
		{name: "entry is not a model", body: `{"object":"list","data":[{"id":"deepseek-flash","object":"engine","owned_by":"deepseek"}]}`, want: ErrUnexpectedResponse},
		{name: "missing id", body: `{"object":"list","data":[{"object":"model","owned_by":"deepseek"}]}`, want: ErrUnexpectedResponse},
		{name: "unsafe id", body: `{"object":"list","data":[{"id":"../../steal","object":"model","owned_by":"deepseek"}]}`, want: ErrUnexpectedResponse},
		{name: "negative metadata", body: `{"object":"list","data":[{"id":"deepseek-flash","object":"model","owned_by":"deepseek","context_window":-1}]}`, want: ErrUnexpectedResponse},
		{name: "null data", body: `{"object":"list","data":null}`, want: ErrUnexpectedResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient(testKey, rtFunc(func(*http.Request) (*http.Response, error) {
				return fakeResponse(http.StatusOK, tc.body), nil
			}))
			models, err := client.ListModels(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v; want %v", err, tc.want)
			}
			if models != nil {
				t.Errorf("models = %v; want nil on error", models)
			}
			if strings.Contains(ErrorMessage(err), testKey) {
				t.Errorf("message leaks key: %q", ErrorMessage(err))
			}
		})
	}
}

func TestListModelsRejectsRedirectWithoutFollowing(t *testing.T) {
	calls := 0
	client := NewClient(testKey, rtFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.deepseek.com" {
			t.Errorf("redirect target was followed: %s", r.URL)
		}
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://evil.example/steal"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}))

	_, err := client.ListModels(context.Background())
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Fatalf("error = %v; want ErrUnexpectedResponse", err)
	}
	if calls != 1 {
		t.Fatalf("transport called %d times; want exactly 1 (redirects must not be followed)", calls)
	}
}

func TestListModelsRejectsOversizedBody(t *testing.T) {
	client := NewClient(testKey, rtFunc(func(*http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusOK, strings.Repeat("a", maxModelsBodyBytes+1)), nil
	}))

	_, err := client.ListModels(context.Background())
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v; want ErrResponseTooLarge", err)
	}
}

func TestErrorMessageMapping(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrTimeout, "did not answer within the time limit"},
		{ErrCanceled, "cancelled"},
		{ErrUnreachable, "could not be reached"},
		{ErrResponseTooLarge, "too large"},
		{ErrUnexpectedResponse, "unexpected response"},
		{errors.New("something unexpected"), "failed"},
	}
	for _, tc := range cases {
		if got := ErrorMessage(tc.err); !strings.Contains(got, tc.want) {
			t.Errorf("ErrorMessage(%v) = %q; want it to contain %q", tc.err, got, tc.want)
		}
	}
}

func TestModelNameTextIsCleaned(t *testing.T) {
	client := NewClient(testKey, rtFunc(func(*http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusOK, `{"object":"list","data":[
			{"id":"deepseek-flash","object":"model","owned_by":"deep\nseek","name":"Flash\u202eName"}]}`), nil
	}))
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if strings.ContainsAny(models[0].OwnedBy, "\n") {
		t.Errorf("ownedBy still contains a newline: %q", models[0].OwnedBy)
	}
	if strings.ContainsRune(models[0].Name, '\u202e') {
		t.Errorf("name still contains a bidi override: %q", models[0].Name)
	}
	if models[0].Name != "FlashName" {
		t.Errorf("name = %q; want the bidi override removed", models[0].Name)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
