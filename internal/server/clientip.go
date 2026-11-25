package server

import (
	"net"
	"net/http"
	"strings"
)

// DefaultTrustedProxies covers loopback only, ensuring safe defaults when
// TRUSTED_PROXIES is unset. Operators behind reverse proxies (like Railway)
// must configure TRUSTED_PROXIES explicitly.
var DefaultTrustedProxies = []string{
	"127.0.0.1/32", "::1/128",
}

// ParseCIDRs is exported so the entry point can parse TRUSTED_PROXIES.
func ParseCIDRs(values []string) []*net.IPNet { return parseCIDRs(values) }

func parseCIDRs(values []string) []*net.IPNet {
	var out []*net.IPNet
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(v); err == nil {
			out = append(out, n)
		} else if ip := net.ParseIP(v); ip != nil {
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return out
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func normalizeIP(s string) net.IP {
	s = strings.TrimSpace(s)
	// SplitHostPort must see the brackets: "[::1]:8080" only splits while
	// bracketed, so stripping them first made every IPv6 RemoteAddr
	// unparseable. Bare IPv6 literals fail to split and are handled below.
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	// Brackets only ever delimit a bare literal ([::1]); once a port is
	// present SplitHostPort has already removed them. Requiring the closing
	// bracket to end the value stops "[::1]junk" from parsing as ::1, which
	// matters because this function also feeds the trusted-proxy check.
	if strings.HasPrefix(s, "[") {
		if end := strings.IndexByte(s, ']'); end > 0 && end == len(s)-1 {
			s = s[1:end]
		}
	}
	// net.ParseIP rejects zone identifiers (fe80::1%eth0), so a zone left
	// in place would make the whole address unparseable and push clientIP
	// back to the raw RemoteAddr with its ephemeral port — one bucket per
	// connection. The zone names a local interface, not a network client,
	// and never contributes to CIDR matching, so it is dropped instead.
	// Consequence: two link-local peers that share an address on different
	// interfaces share one rate-limit bucket. That errs towards a stricter
	// shared limit and cannot be steered by a forwarded header, whereas
	// keeping %zone would let a sender multiply buckets with one header.
	if zone := strings.IndexByte(s, '%'); zone >= 0 {
		s = s[:zone]
	}
	return net.ParseIP(s)
}

// trustedPeer reports whether the immediate connection comes from a
// configured proxy. Shared by clientIP and secureRequest so header trust is
// decided the same way in both places.
func (s *Server) trustedPeer(r *http.Request) bool {
	return ipInNets(normalizeIP(r.RemoteAddr), s.trustedProxies)
}

// clientIP resolves the client IP.
// If CLIENT_IP_HEADER is configured, client identity is taken strictly from
// that header. When unset or the header is absent, it falls back to the direct
// connection address.
func (s *Server) clientIP(r *http.Request) string {
	peer := normalizeIP(r.RemoteAddr)

	if s.clientIPHeader != "" {
		if raw := r.Header.Get(s.clientIPHeader); raw != "" {
			if ip := normalizeIP(raw); ip != nil {
				return ip.String()
			}
		}
		if peer == nil {
			return r.RemoteAddr
		}
		return peer.String()
	}

	peerTrusted := s.trustedPeer(r)
	if s.trustedProxyHeader != "" && (peerTrusted || s.trustHeaderAlways) {
		if ip := normalizeIP(r.Header.Get(s.trustedProxyHeader)); ip != nil {
			return ip.String()
		}
	}
	if !peerTrusted {
		if peer == nil {
			return r.RemoteAddr
		}
		return peer.String()
	}

	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := normalizeIP(parts[i])
		if ip == nil || ipInNets(ip, s.trustedProxies) {
			continue
		}
		return ip.String()
	}
	if peer == nil {
		return r.RemoteAddr
	}
	return peer.String()
}
