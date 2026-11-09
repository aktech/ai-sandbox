package proxy

import (
	"context"
	"errors"
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
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := lookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if wildcard {
		for _, ip := range ips {
			if localAddr(ip) {
				return nil, fmt.Errorf("aisb-proxy: %s is a local address; \"*\" does not cover it, list the host explicitly", host)
			}
		}
	}
	return raceDial(ctx, network, port, ips)
}

// Upstream dial timings. A NAT in front of the proxy (a Docker VM's user-mode
// network) can silently drop the first SYN of a new flow, and the kernel only
// retransmits it after a second or more. So each attempt is short, a second
// attempt on a fresh connection starts raceDelay after the first (RFC 8305's
// connection attempt delay), and the whole dial gives up after dialBudget.
var (
	attemptTimeout = 1500 * time.Millisecond
	raceDelay      = 250 * time.Millisecond
	dialBudget     = 10 * time.Second
)

// maxRacing is how many attempts may be in flight at once.
const maxRacing = 2

// raceDial connects to one of ips on port. IPv4 goes first because a sandbox
// VM often has no IPv6 route. Addresses are tried in turn, round and round: a
// new attempt starts every raceDelay, or as soon as one fails, with at most
// maxRacing in flight; an address that refuses is dropped, one that times out
// stays in the rotation. The first connection wins and any later one is closed.
func raceDial(ctx context.Context, network, port string, ips []netip.Addr) (net.Conn, error) {
	if len(ips) == 0 {
		return nil, fmt.Errorf("aisb-proxy: no addresses for port %s", port)
	}
	plan := append([]netip.Addr(nil), ips...)
	sort.SliceStable(plan, func(i, j int) bool { return plan[i].Unmap().Is4() && !plan[j].Unmap().Is4() })

	ctx, cancel := context.WithTimeout(ctx, dialBudget)
	defer cancel()

	type result struct {
		ip   netip.Addr
		conn net.Conn
		err  error
	}
	results := make(chan result)
	inFlight, next := 0, 0
	launch := func() {
		ip := plan[next%len(plan)]
		next++
		inFlight++
		go func() {
			attempt, stop := context.WithTimeout(ctx, attemptTimeout)
			c, err := dialIP(attempt, network, net.JoinHostPort(ip.Unmap().String(), port))
			stop()
			select {
			case results <- result{ip, c, err}:
			case <-ctx.Done():
				if c != nil {
					c.Close()
				}
			}
		}()
	}

	timer := time.NewTimer(raceDelay)
	defer timer.Stop()
	var last error
	launch()
	for {
		select {
		case r := <-results:
			inFlight--
			if r.err == nil {
				return r.conn, nil
			}
			last = r.err
			if !timedOut(r.err) {
				plan = without(plan, r.ip)
			}
			if len(plan) == 0 {
				if inFlight == 0 {
					return nil, last
				}
				continue
			}
			if ctx.Err() == nil && inFlight < maxRacing {
				launch()
			}
		case <-timer.C:
			if len(plan) > 0 && inFlight < maxRacing {
				launch()
			}
			timer.Reset(raceDelay)
		case <-ctx.Done():
			if last == nil {
				last = ctx.Err()
			}
			return nil, fmt.Errorf("aisb-proxy: no connection within %v: %w", dialBudget, last)
		}
	}
}

// timedOut reports whether err is an attempt that got no answer, as opposed to
// one the upstream refused.
func timedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

func without(ips []netip.Addr, ip netip.Addr) []netip.Addr {
	out := ips[:0:0]
	for _, x := range ips {
		if x != ip {
			out = append(out, x)
		}
	}
	return out
}

// lookupIP and dialIP are the resolver and dialer guardedDial uses; tests replace them.
var (
	lookupIP = net.DefaultResolver.LookupNetIP
	dialIP   = func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
)
