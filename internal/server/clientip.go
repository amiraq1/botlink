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
	s = strings.Trim(s, "[]")
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	return net.ParseIP(s)
}

// trustedPeer reports whether the immediate connection comes from a
// configured proxy. Shared by clientIP and secureRequest so header trust is
// decided the same way in both places.
func (s *Server) trustedPeer(r *http.Request) bool {
	return ipInNets(normalizeIP(r.RemoteAddr), s.trustedProxies)
}

// clientIP only trusts forwarding headers when the immediate peer is a
// configured proxy. The first X-Forwarded-For element is never used.
func (s *Server) clientIP(r *http.Request) string {
	peer := normalizeIP(r.RemoteAddr)
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
