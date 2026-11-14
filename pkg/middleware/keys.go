package middleware

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// KeyFunc extracts a key after any authentication middleware has run.
type KeyFunc func(*http.Request) (string, error)

// Header requires exactly one nonblank header value. It does not authenticate it.
func Header(name string) KeyFunc {
	return func(r *http.Request) (string, error) {
		values := r.Header.Values(name)
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return "", fmt.Errorf("missing or ambiguous %s header", name)
		}
		return strings.TrimSpace(values[0]), nil
	}
}

// ClientIP uses only the socket peer and ignores all forwarding headers.
func ClientIP() KeyFunc { return clientIP(nil) }

// TrustedProxyClientIP trusts X-Forwarded-For only from the given proxy CIDRs.
// It walks from the socket peer right-to-left and stops at the first untrusted
// address. Left-side values supplied by that untrusted hop are ignored.
func TrustedProxyClientIP(cidrs ...string) (KeyFunc, error) {
	trusted := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", cidr, err)
		}
		if prefix.Addr().Is4In6() && prefix.Bits() >= 96 {
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		trusted = append(trusted, prefix.Masked())
	}
	return clientIP(trusted), nil
}

func clientIP(trusted []netip.Prefix) KeyFunc {
	contains := func(ip netip.Addr) bool {
		for _, prefix := range trusted {
			if prefix.Contains(ip) {
				return true
			}
		}
		return false
	}
	return func(r *http.Request) (string, error) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return "", errors.New("invalid socket peer address")
		}
		ip = ip.Unmap().WithZone("")
		values := r.Header.Values("X-Forwarded-For")
		if !contains(ip) || len(values) == 0 {
			return ip.String(), nil
		}
		chain := strings.Join(values, ",")
		if len(chain) > 4096 {
			return "", errors.New("forwarded address chain exceeds 4096 bytes")
		}
		hops := strings.Split(chain, ",")
		if len(hops) > 32 {
			return "", errors.New("forwarded address chain exceeds 32 hops")
		}
		for i := len(hops) - 1; i >= 0 && contains(ip); i-- {
			ip, err = netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				return "", errors.New("invalid forwarded IP address")
			}
			ip = ip.Unmap().WithZone("")
		}
		return ip.String(), nil
	}
}
