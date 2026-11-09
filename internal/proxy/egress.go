package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"time"
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
	if !wildcard {
		d := net.Dialer{Timeout: dialTimeout}
		return d.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := lookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if localAddr(ip) {
			return nil, fmt.Errorf("aisb-proxy: %s is a local address; \"*\" does not cover it, list the host explicitly", host)
		}
	}
	// IPv4 first: a sandbox VM often has no IPv6 route, and a dropped SYN would
	// otherwise hold the request until the OS gives up. Each attempt is bounded.
	sort.SliceStable(ips, func(i, j int) bool { return ips[i].Unmap().Is4() && !ips[j].Unmap().Is4() })
	var last error
	for _, ip := range ips {
		attempt, cancel := context.WithTimeout(ctx, dialTimeout)
		c, err := dialIP(attempt, network, net.JoinHostPort(ip.Unmap().String(), port))
		cancel()
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// dialTimeout bounds each connection attempt to one upstream address.
var dialTimeout = 10 * time.Second

// lookupIP and dialIP are the resolver and dialer guardedDial uses; tests replace them.
var (
	lookupIP = net.DefaultResolver.LookupNetIP
	dialIP   = func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
)
