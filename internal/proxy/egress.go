package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
)

// A wildcard ("*") allow opens the internet, not the proxy's own machine or
// network. Addresses that reach the host, its LAN, cloud metadata
// (169.254.169.254) or other sandboxes are dialed only for a host an entry
// names explicitly. The check runs on the resolved address at dial time and
// the checked address is the one dialed, so a name cannot resolve to a
// local address between check and use.

type ctxKey struct{}

// withWildcardOnly marks a request whose host is allowed only through "*".
func withWildcardOnly(ctx context.Context, wildcard bool) context.Context {
	return context.WithValue(ctx, ctxKey{}, wildcard)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// localAddr reports whether ip belongs to the host, a private network or a
// non-routable range rather than the public internet.
func localAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified() || cgnat.Contains(ip) || (ip.Is4() && ip.As4()[0] == 0)
}

// guardedDial dials addr. When wildcard is set it refuses an address that
// resolves (in any of its records) to a local one, and dials the address it
// checked.
func guardedDial(ctx context.Context, network, addr string, wildcard bool) (net.Conn, error) {
	var d net.Dialer
	if !wildcard {
		return d.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if localAddr(ip) {
			return nil, fmt.Errorf("aisb-proxy: %s is a local address; \"*\" does not cover it, list the host explicitly", host)
		}
	}
	var last error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}
