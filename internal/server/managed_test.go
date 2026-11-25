package server

// Tests for the Phase 2 admin API: connection CRUD, model prices and the
// GET /models probe, all against a fake provider transport. No real provider
// is contacted and no paid request is made.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/amiraq1/botlink/internal/store"
)

const llmTestKey = "sk-test-server-0123456789WXYZ"

type fakeRT func(*http.Request) (*http.Response, error)

func (f fakeRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

const fakeModelsBody = `{"object":"list","data":[
  {"id":"deepseek-flash","object":"model","owned_by":"deepseek","name":"DeepSeek-V4.1-Flash","context_window":1048576,"max_output_tokens":393216},
  {"id":"deepseek-v4-pro","object":"model","owned_by":"deepseek","name":"DeepSeek-V4-Pro"}
]}`

func newManagedAdmin(t *testing.T) (*Server, *store.Store, *http.Cookie) {
	t.Helper()
	srv, st := newTestServer(t, "secret123")
	return srv, st, login(t, srv)
}

func createConnectionViaAPI(t *testing.T, srv *Server, cookie *http.Cookie) store.LLMConnection {
	t.Helper()
	rec := doReq(t, srv, "POST", "/api/admin/llm/connections", map[string]any{
		"provider": "deepseek", "label": "main", "apiKey": llmTestKey,
	}, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var conn store.LLMConnection
	if err := json.Unmarshal(rec.Body.Bytes(), &conn); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return conn
}

func TestLLMConnectionCRUDNeverExposesKey(t *testing.T) {
	srv, st, cookie := newManagedAdmin(t)

	created := createConnectionViaAPI(t, srv, cookie)
	if !created.HasKey || created.KeyLast4 != "WXYZ" {
		t.Fatalf("created HasKey=%v KeyLast4=%q; want true/WXYZ", created.HasKey, created.KeyLast4)
	}

	// The HTTP view never carries key material.
	rec := doReq(t, srv, "GET", "/api/admin/llm/connections", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), llmTestKey) ||
		strings.Contains(strings.ToLower(rec.Body.String()), "apikey") ||
		strings.Contains(strings.ToLower(rec.Body.String()), "api_key") {
		t.Fatalf("connection list leaks key material: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "WXYZ") {
		t.Errorf("connection list should still show the key suffix: %s", rec.Body.String())
	}

	key, err := st.DecryptConnectionKey(context.Background(), created.ID)
	if err != nil || key != llmTestKey {
		t.Fatalf("decrypted key = %q, %v; want the stored key", key, err)
	}

	// A metadata-only edit keeps both the secret and its displayed suffix.
	rec = doReq(t, srv, "PUT", "/api/admin/llm/connections/1", map[string]any{
		"provider": "deepseek", "label": "renamed", "modelHint": "deepseek-flash",
	}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated store.LLMConnection
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.KeyLast4 != "WXYZ" || updated.Label != "renamed" {
		t.Fatalf("updated = %+v; want renamed with KeyLast4 WXYZ", updated)
	}
	if key, err := st.DecryptConnectionKey(context.Background(), created.ID); err != nil || key != llmTestKey {
		t.Fatalf("metadata-only edit changed the key: %q, %v", key, err)
	}

	// A new key replaces the old one and updates the suffix.
	const replacement = "sk-test-server-replacement-QWER"
	rec = doReq(t, srv, "PUT", "/api/admin/llm/connections/1", map[string]any{
		"provider": "deepseek", "label": "renamed", "apiKey": replacement,
	}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("key update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), replacement) {
		t.Fatalf("update response leaks the new key: %s", rec.Body.String())
	}
	if key, err := st.DecryptConnectionKey(context.Background(), created.ID); err != nil || key != replacement {
		t.Fatalf("key after update = %q, %v; want the replacement", key, err)
	}
	if conn, err := st.GetConnection(context.Background(), created.ID); err != nil || conn.KeyLast4 != "QWER" {
		t.Fatalf("key suffix after update = %+v, %v", conn, err)
	}

	// Unknown ids are not created or silently ignored.
	if rec := doReq(t, srv, "DELETE", "/api/admin/llm/connections/999", nil, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("delete unknown status = %d; want 404", rec.Code)
	}
	if rec := doReq(t, srv, "POST", "/api/admin/llm/connections", map[string]any{
		"provider": "openai", "apiKey": llmTestKey,
	}, cookie); rec.Code != http.StatusBadRequest {
		t.Errorf("unsupported provider status = %d; want 400", rec.Code)
	}

	if rec := doReq(t, srv, "DELETE", "/api/admin/llm/connections/1", nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestLLMConnectionCheckUsesStoredKeyAndFixedOrigin(t *testing.T) {
	srv, st, cookie := newManagedAdmin(t)
	conn := createConnectionViaAPI(t, srv, cookie)

	var gotURL, gotAuth string
	srv.llmTransport = fakeRT(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		return fakeJSONResponse(http.StatusOK, fakeModelsBody), nil
	})

	rec := doReq(t, srv, "POST", "/api/admin/llm/connections/1/check", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("check status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), llmTestKey) {
		t.Fatalf("check response leaks the API key: %s", rec.Body.String())
	}
	var result connectionCheckResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode check response: %v", err)
	}
	if result.Status != "ok" || result.Error != "" {
		t.Fatalf("result = %+v; want ok without error", result)
	}
	if len(result.Models) != 2 || result.Models[0].ID != "deepseek-flash" ||
		result.Models[0].ContextWindow != 1048576 {
		t.Fatalf("models = %+v", result.Models)
	}
	if gotURL != "https://api.deepseek.com/models" {
		t.Errorf("provider URL = %q; want the fixed official models URL", gotURL)
	}
	if gotAuth != "Bearer "+llmTestKey {
		t.Errorf("provider Authorization = %q; want the decrypted stored key", gotAuth)
	}
	if result.Connection == nil || result.Connection.LastCheckStatus != "ok" {
		t.Fatalf("check result connection = %+v; want lastCheckStatus ok", result.Connection)
	}
	stored, err := st.GetConnection(context.Background(), conn.ID)
	if err != nil || stored.LastCheckStatus != "ok" || stored.LastCheckedAt == 0 || stored.LastCheckError != "" {
		t.Fatalf("stored connection = %+v, %v; want a recorded successful check", stored, err)
	}
}

func TestLLMConnectionCheckProviderErrorNeverLeaksKey(t *testing.T) {
	srv, st, cookie := newManagedAdmin(t)
	conn := createConnectionViaAPI(t, srv, cookie)

	srv.llmTransport = fakeRT(func(*http.Request) (*http.Response, error) {
		return fakeJSONResponse(http.StatusUnauthorized,
			`{"error":{"message":"Incorrect API key provided: `+llmTestKey+`","code":"invalid_api_key"}}`), nil
	})

	rec := doReq(t, srv, "POST", "/api/admin/llm/connections/1/check", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("check status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, llmTestKey) {
		t.Fatalf("check response leaks the API key: %s", body)
	}
	var result connectionCheckResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode check response: %v", err)
	}
	if result.Status != "error" || !strings.Contains(result.Error, "HTTP 401") {
		t.Fatalf("result = %+v; want an HTTP 401 error", result)
	}
	if len(result.Models) != 0 {
		t.Errorf("models = %+v; want none on error", result.Models)
	}
	stored, err := st.GetConnection(context.Background(), conn.ID)
	if err != nil {
		t.Fatalf("GetConnection: %v", err)
	}
	if stored.LastCheckStatus != "error" || stored.LastCheckError == "" {
		t.Fatalf("stored check = %q/%q; want a recorded error", stored.LastCheckStatus, stored.LastCheckError)
	}
	if strings.Contains(stored.LastCheckError, llmTestKey) {
		t.Fatalf("stored check error leaks the API key: %q", stored.LastCheckError)
	}

	// The list endpoint must not reveal the error text with key material.
	rec = doReq(t, srv, "GET", "/api/admin/llm/connections", nil, cookie)
	if strings.Contains(rec.Body.String(), llmTestKey) {
		t.Fatalf("connection list leaks the API key after a failed check: %s", rec.Body.String())
	}
}

func TestLLMConnectionCheckUnexpectedResponseAndTimeout(t *testing.T) {
	cases := []struct {
		name     string
		rt       fakeRT
		wantText string
	}{
		{
			name: "unexpected body",
			rt: func(*http.Request) (*http.Response, error) {
				return fakeJSONResponse(http.StatusOK, `{"object":"list","data":[]}`), nil
			},
			wantText: "unexpected response",
		},
		{
			name: "timeout",
			rt: func(*http.Request) (*http.Response, error) {
				return nil, context.DeadlineExceeded
			},
			wantText: "time limit",
		},
		{
			name: "unreachable",
			rt: func(*http.Request) (*http.Response, error) {
				return nil, io.ErrUnexpectedEOF
			},
			wantText: "could not be reached",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, cookie := newManagedAdmin(t)
			createConnectionViaAPI(t, srv, cookie)
			srv.llmTransport = tc.rt

			rec := doReq(t, srv, "POST", "/api/admin/llm/connections/1/check", nil, cookie)
			if rec.Code != http.StatusOK {
				t.Fatalf("check status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var result connectionCheckResult
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatalf("decode check response: %v", err)
			}
			if result.Status != "error" || !strings.Contains(result.Error, tc.wantText) {
				t.Fatalf("result = %+v; want an error containing %q", result, tc.wantText)
			}
			if strings.Contains(result.Error, llmTestKey) {
				t.Errorf("error leaks the API key: %q", result.Error)
			}
		})
	}
}

func TestModelPriceAdminAPI(t *testing.T) {
	srv, _, cookie := newManagedAdmin(t)
	createConnectionViaAPI(t, srv, cookie)

	rec := doReq(t, srv, "PUT", "/api/admin/llm/connections/1/prices/deepseek-flash", map[string]any{
		"inputMicrosPer1M": 270000, "outputMicrosPer1M": 1100000, "currency": "USD",
	}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("set price status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var price store.ModelPrice
	if err := json.Unmarshal(rec.Body.Bytes(), &price); err != nil {
		t.Fatalf("decode price: %v", err)
	}
	if price.Model != "deepseek-flash" || price.InputMicrosPer1M != 270000 ||
		price.OutputMicrosPer1M != 1100000 || price.Currency != "USD" {
		t.Fatalf("price = %+v", price)
	}

	rec = doReq(t, srv, "GET", "/api/admin/llm/connections/1/prices", nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "deepseek-flash") {
		t.Fatalf("list prices = %d %s", rec.Code, rec.Body.String())
	}

	if rec := doReq(t, srv, "PUT", "/api/admin/llm/connections/1/prices/deepseek-v4-pro", map[string]any{
		"inputMicrosPer1M": -1, "outputMicrosPer1M": 0,
	}, cookie); rec.Code != http.StatusBadRequest {
		t.Errorf("negative price status = %d; want 400", rec.Code)
	}
	if rec := doReq(t, srv, "PUT", "/api/admin/llm/connections/999/prices/deepseek-flash", map[string]any{
		"inputMicrosPer1M": 1, "outputMicrosPer1M": 1,
	}, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("price on unknown connection status = %d; want 404", rec.Code)
	}
	if rec := doReq(t, srv, "GET", "/api/admin/llm/connections/999/prices", nil, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("prices of unknown connection status = %d; want 404", rec.Code)
	}

	if rec := doReq(t, srv, "DELETE", "/api/admin/llm/connections/1/prices/deepseek-flash", nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("delete price status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, srv, "DELETE", "/api/admin/llm/connections/1/prices/deepseek-flash", nil, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("second delete price status = %d; want 404", rec.Code)
	}
	rec = doReq(t, srv, "GET", "/api/admin/llm/connections/1/prices", nil, cookie)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("prices after delete = %d %q; want []", rec.Code, rec.Body.String())
	}
}
