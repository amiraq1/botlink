package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/amiraq1/botlink/internal/store"
)

var (
	// Sentinel errors for webhook execution
	ErrWebhookTimeout         = errors.New("webhook request timed out")
	ErrWebhookCanceled        = errors.New("webhook request canceled")
	ErrWebhookBlocked         = errors.New("webhook destination blocked")
	ErrWebhookPayloadTooLarge = errors.New("webhook response too large")
)

const maxWebhookResponseSize = 64 * 1024

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),          // "This host on this network"
	netip.MustParsePrefix("100.64.0.0/10"),      // Shared Address Space / CGNAT
	netip.MustParsePrefix("169.254.0.0/16"),     // Link Local / Cloud Metadata
	netip.MustParsePrefix("192.0.0.0/24"),       // IETF Protocol Assignments
	netip.MustParsePrefix("198.18.0.0/15"),      // Benchmark Testing
	netip.MustParsePrefix("240.0.0.0/4"),        // Reserved for future use
	netip.MustParsePrefix("255.255.255.255/32"), // Limited broadcast
	netip.MustParsePrefix("64:ff9b::/96"),       // IPv4/IPv6 translation (NAT64 Well-Known)
	netip.MustParsePrefix("64:ff9b:1::/48"),     // IPv4/IPv6 translation (NAT64 Local)
	netip.MustParsePrefix("2002::/16"),          // 6to4 transition
	netip.MustParsePrefix("2001::/32"),          // Teredo tunneling
	netip.MustParsePrefix("fec0::/10"),          // Deprecated Site-Local
}

// isBlockedIP determines if an IP address is private, loopback, link-local,
// metadata, CGNAT, multicast, unspecified, reserved, or special-purpose.
// It explicitly unmaps IPv4-mapped IPv6 addresses (e.g. ::ffff:127.0.0.1 -> 127.0.0.1).
func isBlockedIP(addr netip.Addr, allowPrivateHosts bool) bool {
	addr = addr.Unmap()
	if allowPrivateHosts {
		return false
	}
	if !addr.IsValid() {
		return true
	}
	// Must be global unicast, and not private/loopback/link-local/multicast/unspecified.
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// SafeWebhookClient provides an SSRF-hardened HTTP client for webhook tool calls.
type SafeWebhookClient struct {
	client            *http.Client
	allowPrivateHosts bool
	allowHTTP         bool
}

// NewSafeWebhookClient creates a new hardened SafeWebhookClient.
func NewSafeWebhookClient(allowPrivateHosts, allowHTTP bool, _ time.Duration) *SafeWebhookClient {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid address: %w", err)
			}

			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("dns resolution failed: %w", err)
			}
			if len(ips) == 0 {
				return nil, errors.New("no IP addresses found for host")
			}

			// Validate ALL resolved IP addresses. If ANY address resolves to a blocked range
			// or cannot be parsed, reject the request immediately to prevent DNS rebinding
			// or dual-stack bypass tricks.
			var validIPs []netip.Addr
			for _, ip := range ips {
				parsed, ok := netip.AddrFromSlice(ip)
				if !ok {
					return nil, fmt.Errorf("invalid resolved IP format: %w", ErrWebhookBlocked)
				}
				if isBlockedIP(parsed, allowPrivateHosts) {
					return nil, fmt.Errorf("connection to blocked IP address (%s) is prohibited: %w", parsed.String(), ErrWebhookBlocked)
				}
				validIPs = append(validIPs, parsed)
			}

			if len(validIPs) == 0 {
				return nil, fmt.Errorf("no valid IP addresses resolved: %w", ErrWebhookBlocked)
			}

			// Dual-stack resilient connection: iterate through resolved valid IPs sequentially
			// until one connects successfully (e.g. if IPv6 is unreachable, try IPv4).
			var conn net.Conn
			var lastErr error
			for _, ip := range validIPs {
				targetAddr := net.JoinHostPort(ip.String(), port)
				c, dialErr := dialer.DialContext(ctx, network, targetAddr)
				if dialErr == nil {
					conn = c
					break
				}
				lastErr = dialErr
			}

			if conn == nil {
				if lastErr != nil {
					return nil, lastErr
				}
				return nil, errors.New("failed to connect to host")
			}

			// Post-connect check on the actual remote socket
			if tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
				if connIP, ok := netip.AddrFromSlice(tcpAddr.IP); ok {
					if isBlockedIP(connIP, allowPrivateHosts) {
						_ = conn.Close()
						return nil, fmt.Errorf("connection to blocked IP address (%s) is prohibited: %w", connIP.String(), ErrWebhookBlocked)
					}
				}
			}

			return conn, nil
		},
		MaxIdleConns:          50,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &SafeWebhookClient{
		allowPrivateHosts: allowPrivateHosts,
		allowHTTP:         allowHTTP,
		client: &http.Client{
			Transport: transport,
			Timeout:   0, // Individual request timeouts are enforced per-tool via context.WithTimeout
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Prohibit following HTTP redirects
				return http.ErrUseLastResponse
			},
		},
	}
}

// WebhookRequest encapsulates a webhook invocation.
type WebhookRequest struct {
	URL       string
	Headers   map[string]string
	Secret    string
	Payload   []byte
	TimeoutMs int64
	GroupID   int64
	BotID     string
	ToolID    int64
	ToolName  string
}

// Execute performs the webhook request with SSRF protection, signing, and response capping.
func (c *SafeWebhookClient) Execute(ctx context.Context, req WebhookRequest) (string, error) {
	u, err := url.Parse(req.URL)
	if err != nil {
		return "", fmt.Errorf("invalid webhook URL")
	}

	// 1. URL structural validation
	if u.Hostname() == "" {
		return "", fmt.Errorf("webhook URL must include a hostname")
	}
	if u.User != nil {
		return "", fmt.Errorf("webhook URL must not contain user credentials")
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" {
		if !c.allowHTTP || scheme != "http" {
			return "", fmt.Errorf("webhook URL must use https scheme")
		}
	}

	// 2. Per-tool timeout enforcement with safety bounds [500ms, 30000ms]
	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 10_000
	} else if timeoutMs < 500 {
		timeoutMs = 500
	} else if timeoutMs > 30_000 {
		timeoutMs = 30_000
	}

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, req.URL, bytes.NewReader(req.Payload))
	if err != nil {
		return "", fmt.Errorf("create request failed")
	}

	// 3. Custom headers (decrypted), strictly blocking system/protocol headers
	for k, v := range req.Headers {
		if strings.EqualFold(k, "Host") ||
			strings.EqualFold(k, "Content-Length") ||
			strings.EqualFold(k, "Transfer-Encoding") ||
			strings.EqualFold(k, "X-Botlink-Timestamp") ||
			strings.EqualFold(k, "X-Botlink-Signature") ||
			strings.EqualFold(k, "Content-Type") ||
			strings.EqualFold(k, "User-Agent") {
			continue
		}
		httpReq.Header.Set(k, v)
	}

	// Guarantee no leaked or spoofed signature/timestamp if secret is empty
	httpReq.Header.Del("X-Botlink-Timestamp")
	httpReq.Header.Del("X-Botlink-Signature")

	// 4. Fixed system headers set AFTER custom headers
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")
	httpReq.Header.Set("User-Agent", "botlink-webhook/1.0")

	// HMAC-SHA256 timestamp and signature (only when secret is configured)
	nowMs := time.Now().UnixMilli()
	tsStr := strconv.FormatInt(nowMs, 10)
	httpReq.Header.Set("X-Botlink-Timestamp", tsStr)

	if req.Secret != "" {
		mac := hmac.New(sha256.New, []byte(req.Secret))
		mac.Write([]byte(tsStr))
		mac.Write([]byte("."))
		mac.Write(req.Payload)
		sig := hex.EncodeToString(mac.Sum(nil))
		httpReq.Header.Set("X-Botlink-Signature", "hmac-sha256="+sig)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		if errors.Is(reqCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return "", ErrWebhookTimeout
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return "", ErrWebhookCanceled
		}
		if errors.Is(err, ErrWebhookBlocked) {
			return "", ErrWebhookBlocked
		}
		// Never leak internal DNS/network errors or raw IP addresses
		return "", errors.New("webhook request failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return "", errors.New("webhook returned redirect, which is prohibited")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("webhook returned non-2xx status code: %d", resp.StatusCode)
	}

	// Limit response body reading to maxWebhookResponseSize + 1 to detect truncation
	limitReader := io.LimitReader(resp.Body, maxWebhookResponseSize+1)
	bodyBytes, err := io.ReadAll(limitReader)
	if err != nil {
		return "", errors.New("failed to read webhook response")
	}
	if len(bodyBytes) > maxWebhookResponseSize {
		return "", ErrWebhookPayloadTooLarge
	}

	return string(bodyBytes), nil
}

// SanitizeToolOutput ensures that tool outputs are valid JSON, with all text fields
// sanitized through store.Sanitize() (stripping bidi/invisible characters and replacing links).
func SanitizeToolOutput(raw string) string {
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		sanitized := sanitizeJSONValue(parsed)
		if b, err := json.Marshal(sanitized); err == nil {
			return string(b)
		}
	}

	// If not valid JSON or failed to marshal, wrap in {"text": "<sanitized>"}
	cleanText := store.Sanitize(raw)
	b, _ := json.Marshal(map[string]string{"text": cleanText})
	return string(b)
}

func sanitizeJSONValue(v any) any {
	switch val := v.(type) {
	case string:
		return store.Sanitize(val)
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[store.Sanitize(k)] = sanitizeJSONValue(item)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = sanitizeJSONValue(item)
		}
		return out
	default:
		return val
	}
}

// AllowPrivateHostsConfig determines whether private hosts are permitted (dev only).
func AllowPrivateHostsConfig() bool {
	return os.Getenv("TOOLS_ALLOW_PRIVATE_HOSTS") == "1" && os.Getenv("BOTLINK_DEV") == "1"
}

// AllowHTTPConfig determines whether plain HTTP is permitted for webhooks.
func AllowHTTPConfig(production bool) bool {
	if production {
		return false
	}
	return os.Getenv("TOOLS_ALLOW_HTTP") == "1" || os.Getenv("BOTLINK_DEV") == "1"
}
