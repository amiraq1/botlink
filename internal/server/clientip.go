package server

import (
	"net"
	"net/http"
	"strings"
)

// DefaultTrustedProxies covers loopback, RFC1918 and IPv6 ULA, which are the
// addresses used by Caddy on the same host and by the Fly proxy hop.
var DefaultTrustedProxies = []string{
	"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"::1/128", "fc00::/7",
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

// clientIP only trusts forwarding headers when the immediate peer is a
// configured proxy. The first X-Forwarded-For element is never used.
func (s *Server) clientIP(r *http.Request) string {
	peer := normalizeIP(r.RemoteAddr)
	peerTrusted := ipInNets(peer, s.trustedProxies)

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
