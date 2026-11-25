package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNormalizeIP pins down address parsing for the forms the server can see:
// RemoteAddr always carries a port, forwarding headers may or may not, and
// IPv6 literals are bracketed only when a port is present.
func TestNormalizeIP(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" means normalizeIP must return nil
	}{
		{"ipv4 bare", "127.0.0.1", "127.0.0.1"},
		{"ipv4 with port", "127.0.0.1:8080", "127.0.0.1"},
		{"ipv4 padded", "  203.0.113.7  ", "203.0.113.7"},
		{"ipv4 non numeric port", "127.0.0.1:http", "127.0.0.1"},
		{"ipv4 mapped ipv6", "::ffff:192.0.2.5", "192.0.2.5"},
		{"ipv6 bare", "2001:db8::1", "2001:db8::1"},
		{"ipv6 bracketed", "[2001:db8::1]", "2001:db8::1"},
		{"ipv6 bracketed with port", "[2001:db8::1]:443", "2001:db8::1"},
		{"ipv6 loopback bare", "::1", "::1"},
		{"ipv6 loopback bracketed", "[::1]", "::1"},
		{"ipv6 loopback with port", "[::1]:5199", "::1"},
		{"ipv6 zone with port", "[fe80::1%eth0]:8080", "fe80::1"},
		{"ipv6 zone bare", "fe80::1%3", "fe80::1"},
		{"ipv6 zone bracketed", "[fe80::1%eth0]", "fe80::1"},
		// Brackets only delimit a literal: anything after the closing one
		// means the value is not an address, and must not parse to the
		// literal behind the bracket.
		{"ipv6 junk after bracket", "[::1]junk", ""},
		{"ipv6 junk after bracket with port", "[::1]junk:8080", ""},
		{"ipv4 junk after bracket", "[127.0.0.1]junk", ""},
		{"zone outside bracket", "[fe80::1]%eth0", ""},
		{"forwarded list entry", " 5.6.7.8 ", "5.6.7.8"},
		{"empty", "", ""},
		{"not an ip", "not-an-ip", ""},
		{"hostname", "localhost:8080", ""},
		{"missing closing bracket", "[::1", ""},
		{"stray closing bracket", "::1]", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeIP(tc.in)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("normalizeIP(%q) = %v; want nil", tc.in, got)
				}
				return
			}
			if got == nil || got.String() != tc.want {
				t.Fatalf("normalizeIP(%q) = %v; want %s", tc.in, got, tc.want)
			}
		})
	}
}

func newIPv6ProxyRequest(t *testing.T, port int) *http.Request {
	t.Helper()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = fmt.Sprintf("[::1]:%d", port)
	return req
}

// TestClientIPStableForIPv6ProxyConnections reproduces the Vite dev proxy
// case: the proxy connects over IPv6 loopback from a fresh ephemeral port on
// every request, so the derived client identity must not include the port.
func TestClientIPStableForIPv6ProxyConnections(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.trustedProxies = parseCIDRs(DefaultTrustedProxies)

	first := srv.clientIP(newIPv6ProxyRequest(t, 51001))
	second := srv.clientIP(newIPv6ProxyRequest(t, 51002))

	if first != "::1" || second != "::1" {
		t.Fatalf("clientIP over IPv6 proxy = %q and %q; want ::1 for both", first, second)
	}
	if first != second {
		t.Fatalf("client identity changed between connections from the same address: %q != %q", first, second)
	}

	// The peer is loopback, i.e. a trusted proxy: a forwarded client address
	// must be honoured, including a bracketed IPv6 literal with a port.
	req := newIPv6ProxyRequest(t, 51003)
	req.Header.Set("X-Forwarded-For", "[2001:db8::7]:4444")
	if got := srv.clientIP(req); got != "2001:db8::7" {
		t.Fatalf("clientIP with forwarded IPv6 = %q; want 2001:db8::7", got)
	}
}

// TestRateLimitAppliesAcrossIPv6ProxyConnections asserts the per-IP limits
// actually engage when every request arrives on a new connection, which is
// how a reverse proxy such as the Vite dev server talks to the API.
func TestRateLimitAppliesAcrossIPv6ProxyConnections(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	defer func() {
		srv.Close()
		st.Close()
	}()
	srv.trustedProxies = parseCIDRs(DefaultTrustedProxies)

	// badTokenLimiter allows 15 attempts per minute per client.
	for i := 0; i < 15; i++ {
		req := httptest.NewRequest("GET", "/api/room/badtoken"+fmt.Sprint(i), nil)
		req.RemoteAddr = fmt.Sprintf("[::1]:%d", 53000+i)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d status = %d; want 200", i+1, rec.Code)
		}
	}

	req := httptest.NewRequest("GET", "/api/room/badtoken15", nil)
	req.RemoteAddr = "[::1]:53500"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("16th attempt over IPv6 proxy status = %d; want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After header")
	}
}

// TestLoginRateLimitAcrossIPv6ProxyConnections covers the same identity path
// through the login limiter (10 successes per minute per IP).
func TestLoginRateLimitAcrossIPv6ProxyConnections(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	defer func() {
		srv.Close()
		st.Close()
	}()
	srv.trustedProxies = parseCIDRs(DefaultTrustedProxies)

	login := func(port int) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"secret123"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = fmt.Sprintf("[::1]:%d", port)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < 10; i++ {
		if rec := login(54000 + i); rec.Code != http.StatusOK {
			t.Fatalf("login %d status = %d (%s); want 200", i+1, rec.Code, rec.Body.String())
		}
	}
	if rec := login(54500); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th login over IPv6 proxy status = %d; want 429", rec.Code)
	}
}

// TestTrustedProxyHeadersWithIPv6Peers keeps the trust boundary intact:
// bracketed IPv6 peers must be recognised as loopback proxies, while remote
// IPv6 peers must never have their forwarding headers believed.
func TestTrustedProxyHeadersWithIPv6Peers(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.trustedProxies = parseCIDRs(DefaultTrustedProxies)

	trusted := newIPv6ProxyRequest(t, 55001)
	trusted.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := srv.clientIP(trusted); got != "5.6.7.8" {
		t.Fatalf("trusted IPv6 peer clientIP = %q; want rightmost untrusted hop 5.6.7.8", got)
	}

	untrusted := httptest.NewRequest("GET", "/healthz", nil)
	untrusted.RemoteAddr = "[2001:db8::50]:4321"
	untrusted.Header.Set("X-Forwarded-For", "10.0.0.1")
	if got := srv.clientIP(untrusted); got != "2001:db8::50" {
		t.Fatalf("untrusted IPv6 peer clientIP = %q; want 2001:db8::50", got)
	}

	// A configured edge header is also only read from a trusted peer.
	srv.trustedProxyHeader = "Fly-Client-IP"
	trusted.Header.Set("Fly-Client-IP", "198.51.100.7")
	if got := srv.clientIP(trusted); got != "198.51.100.7" {
		t.Fatalf("trusted IPv6 peer with edge header = %q; want 198.51.100.7", got)
	}
	untrusted.Header.Set("Fly-Client-IP", "198.51.100.7")
	if got := srv.clientIP(untrusted); got != "2001:db8::50" {
		t.Fatalf("untrusted IPv6 peer spoofed edge header = %q; want 2001:db8::50", got)
	}

	// CLIENT_IP_HEADER mode must normalise bracketed IPv6 values too.
	srv.clientIPHeader = "X-Real-IP"
	req := newIPv6ProxyRequest(t, 55002)
	req.Header.Set("X-Real-IP", "[2001:db8::9]:443")
	if got := srv.clientIP(req); got != "2001:db8::9" {
		t.Fatalf("CLIENT_IP_HEADER with bracketed IPv6 = %q; want 2001:db8::9", got)
	}
}

// TestClientIPFallbackForUnparseableRemoteAddr documents the fallback: when
// the address cannot be parsed the raw RemoteAddr is returned unchanged and no
// panic occurs.
func TestClientIPFallbackForUnparseableRemoteAddr(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "not-an-address"
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.Header.Set("X-Real-IP", "10.0.0.1")
	if got := srv.clientIP(req); got != "not-an-address" {
		t.Fatalf("clientIP for unparseable RemoteAddr = %q; want raw RemoteAddr", got)
	}
}

// TestNormalizeIPZoneIdentifiers pins the zone decision. Zones only carry
// meaning for link-local peers on the machine that received the packet; a
// remote client address has none. They must therefore be dropped from the
// identity, but that has a deliberate cost: one link-local address on two
// interfaces collapses to a single identity.
func TestNormalizeIPZoneIdentifiers(t *testing.T) {
	tests := []struct{ in, want string }{
		{"[fe80::1%eth0]:8080", "fe80::1"},
		{"fe80::1%3", "fe80::1"},
		{"[::1%lo]:5199", "::1"},
		{"::ffff:192.0.2.5%eth0", "192.0.2.5"},
	}
	for _, tc := range tests {
		if got := normalizeIP(tc.in); got == nil || got.String() != tc.want {
			t.Fatalf("normalizeIP(%q) = %v; want %s", tc.in, got, tc.want)
		}
	}

	// Documented trade-off: the zone names a local interface, not a client,
	// so the same address on two interfaces yields one bucket. Sharing a
	// stricter limit is preferable to a per-connection identity.
	a, b := normalizeIP("fe80::1%eth0"), normalizeIP("fe80::1%eth1")
	if a == nil || b == nil || !a.Equal(b) {
		t.Fatalf("zones must not split one link-local address: %v vs %v", a, b)
	}

	if got := normalizeIP("%eth0"); got != nil {
		t.Fatalf("zone-only input = %v; want nil", got)
	}
}

// TestLinkLocalPeersWithZonesShareOneBucket shows the rate-limit consequence
// of dropping zones: two interfaces that present the same link-local address
// consume one shared allowance rather than one allowance each.
func TestLinkLocalPeersWithZonesShareOneBucket(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	defer func() {
		srv.Close()
		st.Close()
	}()
	srv.trustedProxies = parseCIDRs(DefaultTrustedProxies)

	for i := 0; i < 15; i++ {
		req := httptest.NewRequest("GET", "/api/room/linklocal"+fmt.Sprint(i), nil)
		req.RemoteAddr = fmt.Sprintf("[fe80::1%%eth%d]:%d", i%2, 56000+i)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d status = %d; want 200", i+1, rec.Code)
		}
	}

	req := httptest.NewRequest("GET", "/api/room/linklocal15", nil)
	req.RemoteAddr = "[fe80::1%eth1]:56999"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("same link-local address on another interface status = %d; want shared 429", rec.Code)
	}
}

// TestForwardedZoneCannotMultiplyBuckets checks the opposite direction: a
// sender that varies the zone on a forwarded address must not get a fresh
// rate-limit bucket per spelling, because the zone is stripped before the
// identity is used as a map key.
func TestForwardedZoneCannotMultiplyBuckets(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	defer func() {
		srv.Close()
		st.Close()
	}()
	srv.trustedProxies = parseCIDRs(DefaultTrustedProxies)

	for i := 0; i < 15; i++ {
		req := httptest.NewRequest("GET", "/api/room/zoned"+fmt.Sprint(i), nil)
		req.RemoteAddr = fmt.Sprintf("[::1]:%d", 57000+i)
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.9%%zone%d", i))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d status = %d; want 200", i+1, rec.Code)
		}
	}

	// The plain spelling must already be exhausted by the zoned spellings.
	req := httptest.NewRequest("GET", "/api/room/zoned15", nil)
	req.RemoteAddr = "[::1]:57999"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("plain address after zoned budget status = %d; want 429", rec.Code)
	}
}

// TestXForwardedForChainWithMultipleTrustedHops covers chains longer than one
// proxy: every trusted hop is skipped from the right until the nearest
// untrusted address, and a chain made only of trusted addresses falls back to
// the connection peer.
func TestXForwardedForChainWithMultipleTrustedHops(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.trustedProxies = parseCIDRs([]string{
		"127.0.0.1/32", "10.0.0.5/32", "192.168.0.0/16", "2001:db8:ffff::/48",
	})

	tests := []struct{ name, xff, want string }{
		{"single hop", "198.51.100.9", "198.51.100.9"},
		{"two proxy hops", "198.51.100.9, 10.0.0.5", "198.51.100.9"},
		{"three proxy hops", "198.51.100.9, 10.0.0.5, 192.168.4.4", "198.51.100.9"},
		{"ipv6 client behind ipv6 proxy", "2001:db8::9, 2001:db8:ffff::1", "2001:db8::9"},
		{"ipv4 mapped client", "::ffff:198.51.100.9, 10.0.0.5", "198.51.100.9"},
		{"zone on client hop", "198.51.100.9%eth0, 10.0.0.5", "198.51.100.9"},
		{"empty entries skipped", " , 198.51.100.9 , ", "198.51.100.9"},
		{"garbage right of client skipped", "198.51.100.9, not-an-ip", "198.51.100.9"},
		{"all hops trusted", "10.0.0.5, 192.168.4.4", "127.0.0.1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := newIPv6ProxyRequest(t, 58001)
			req.RemoteAddr = "127.0.0.1:58001"
			req.Header.Set("X-Forwarded-For", tc.xff)
			if got := srv.clientIP(req); got != tc.want {
				t.Fatalf("clientIP(XFF=%q) = %q; want %q", tc.xff, got, tc.want)
			}
		})
	}
}

// TestTrustedProxyHeaderFallsBackWhenInvalid keeps the configured edge header
// from becoming a single point of failure: a trusted peer with an unusable
// header value still resolves through the proxy chain.
func TestTrustedProxyHeaderFallsBackWhenInvalid(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.trustedProxies = parseCIDRs(DefaultTrustedProxies)
	srv.trustedProxyHeader = "Fly-Client-IP"

	req := newIPv6ProxyRequest(t, 58500)
	req.Header.Set("Fly-Client-IP", "unknown")
	req.Header.Set("X-Forwarded-For", "198.51.100.4")
	if got := srv.clientIP(req); got != "198.51.100.4" {
		t.Fatalf("invalid edge header clientIP = %q; want XFF 198.51.100.4", got)
	}

	// trustHeaderAlways is the documented Fly edge exception: the peer is not
	// in TRUSTED_PROXIES, so only the explicit switch may honour the header.
	srv.trustHeaderAlways = true
	req = httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "[2001:db8::60]:4321"
	req.Header.Set("Fly-Client-IP", "198.51.100.8")
	if got := srv.clientIP(req); got != "198.51.100.8" {
		t.Fatalf("trustHeaderAlways clientIP = %q; want 198.51.100.8", got)
	}

	// The switch must not make XFF trusted: only the named header is read.
	req.Header.Del("Fly-Client-IP")
	req.Header.Set("X-Forwarded-For", "198.51.100.8")
	if got := srv.clientIP(req); got != "2001:db8::60" {
		t.Fatalf("trustHeaderAlways with XFF only = %q; want peer 2001:db8::60", got)
	}
}

// TestTrustedPeerIgnoresForwardedAddresses pins the trust root: a peer is a
// proxy only because of its connection address, never because a header says
// it is one.
func TestTrustedPeerIgnoresForwardedAddresses(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.trustedProxies = parseCIDRs(DefaultTrustedProxies)

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "[2001:db8::70]:4321"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Real-IP", "::1")
	if srv.trustedPeer(req) {
		t.Fatal("untrusted peer became trusted through forwarded headers")
	}
	if got := srv.clientIP(req); got != "2001:db8::70" {
		t.Fatalf("clientIP = %q; want peer 2001:db8::70", got)
	}

	trusted := httptest.NewRequest("GET", "/healthz", nil)
	trusted.RemoteAddr = "[::1%lo]:4321"
	if !srv.trustedPeer(trusted) {
		t.Fatal("kernel-supplied loopback zone must still match ::1/128")
	}
}

// TestJunkAddressCannotSatisfyTrust checks the trust boundary itself. Only
// RemoteAddr can make a peer a proxy; forwarded headers are identity input at
// most, so a malformed bracket string must never become a trusted loopback.
func TestJunkAddressCannotSatisfyTrust(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.trustedProxies = parseCIDRs(DefaultTrustedProxies)
	srv.trustedProxyHeader = "Fly-Client-IP"

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "198.51.100.20:1234"
	req.Header.Set("X-Forwarded-For", "[::1]junk")
	req.Header.Set("X-Real-IP", "[::1]junk")
	req.Header.Set("Fly-Client-IP", "[::1]junk")
	req.Header.Set("X-Forwarded-Proto", "https")
	if srv.trustedPeer(req) {
		t.Fatal("junk forwarded headers made an untrusted peer a proxy")
	}
	if got := srv.clientIP(req); got != "198.51.100.20" {
		t.Fatalf("clientIP = %q; want untrusted peer 198.51.100.20", got)
	}
	if srv.secureRequest(req) {
		t.Fatal("junk headers granted a Secure cookie on an untrusted peer")
	}

	// RemoteAddr is kernel-generated in production, so this shape cannot occur
	// today. It still must not parse as trusted ::1: the trust boundary must
	// not depend on every future caller keeping that invariant.
	junkPeer := httptest.NewRequest("GET", "/healthz", nil)
	junkPeer.RemoteAddr = "[::1]junk:1234"
	if srv.trustedPeer(junkPeer) {
		t.Fatal("junk peer string parsed into trusted ::1")
	}
	if got := srv.clientIP(junkPeer); got != "[::1]junk:1234" {
		t.Fatalf("clientIP for junk peer = %q; want raw RemoteAddr fallback", got)
	}
}

// TestJunkClientIPHeaderFallsBackToPeer keeps a malformed configured header
// from collapsing into a valid-looking identity; the real peer is used instead.
func TestJunkClientIPHeaderFallsBackToPeer(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.clientIPHeader = "X-Real-IP"

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "198.51.100.30:4444"
	req.Header.Set("X-Real-IP", "[::1]junk")
	if got := srv.clientIP(req); got != "198.51.100.30" {
		t.Fatalf("clientIP for junk header = %q; want peer 198.51.100.30", got)
	}

	req.Header.Set("X-Real-IP", "[2001:db8::1]:443")
	if got := srv.clientIP(req); got != "2001:db8::1" {
		t.Fatalf("clientIP for valid bracketed header = %q; want 2001:db8::1", got)
	}
}

// TestJunkForwardedHopIsIgnored treats a malformed hop as unusable rather than
// as the address behind the bracket, so it can neither be selected as the
// client identity nor skip past a real untrusted hop.
func TestJunkForwardedHopIsIgnored(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.trustedProxies = parseCIDRs(DefaultTrustedProxies)

	req := newIPv6ProxyRequest(t, 59001)
	req.Header.Set("X-Forwarded-For", "[::1]junk, 203.0.113.7")
	if got := srv.clientIP(req); got != "203.0.113.7" {
		t.Fatalf("clientIP past junk hop = %q; want 203.0.113.7", got)
	}

	// With no usable hop left, identity falls back to the trusted peer rather
	// than to the literal hidden behind the junk bracket.
	req.Header.Set("X-Forwarded-For", "[203.0.113.9]junk")
	if got := srv.clientIP(req); got != "::1" {
		t.Fatalf("clientIP for junk-only chain = %q; want peer ::1", got)
	}
}

// TestSecureCookieTrustWithIPv6Peers covers the cookie flag decision for the
// same peers: only a trusted proxy may assert X-Forwarded-Proto: https, and
// the zone decision must not widen that rule.
func TestSecureCookieTrustWithIPv6Peers(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.trustedProxies = parseCIDRs(DefaultTrustedProxies)

	cookieSecure := func(remoteAddr, proto string) bool {
		req := httptest.NewRequest("POST", "/api/auth/login", nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set("X-Forwarded-Proto", proto)
		w := httptest.NewRecorder()
		srv.setSessionCookie(w, req, "value")
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("setSessionCookie set %d cookies; want 1", len(cookies))
		}
		return cookies[0].Secure
	}

	if cookieSecure("[::1]:4321", "https") != true {
		t.Error("trusted IPv6 proxy with https proto must set Secure")
	}
	if cookieSecure("[::1]:4321", "http") != false {
		t.Error("trusted IPv6 proxy with http proto must not set Secure")
	}
	if cookieSecure("[::1%lo]:4321", "https") != true {
		t.Error("trusted loopback zone peer must set Secure over https proto")
	}
	if cookieSecure("[2001:db8::80]:4321", "https") != false {
		t.Error("untrusted IPv6 peer must not set Secure from a header")
	}
}
