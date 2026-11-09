package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/elazarl/goproxy"
)

// Server is the credential-injecting egress proxy. Both its secret values and
// its rule set can be hot-swapped at runtime (UpdateSecrets, UpdateRules), so
// a refreshed token or a changed allowlist takes effect on the next request
// without restarting the proxy.
type Server struct {
	proxy   *goproxy.ProxyHttpServer
	secrets atomic.Pointer[map[string]string]
	rules   atomic.Pointer[compiledRules]

	secretsMu sync.Mutex // serialises writers of secrets
	onRefresh atomic.Pointer[func(map[string]string)]

	log *slog.Logger

	mitm   *goproxy.ConnectAction
	tunnel *goproxy.ConnectAction
	reject *goproxy.ConnectAction
}

// New builds a proxy with the given secrets and CA. It starts with an empty
// rule set (deny-all) until UpdateRules is called. caCertPEM/caKeyPEM sign the
// per-host leaf certs goproxy mints when it MITMs an inject host.
func New(secrets map[string]string, caCertPEM, caKeyPEM []byte) (*Server, error) {
	caCert, err := tls.X509KeyPair(caCertPEM, caKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("load CA keypair: %w", err)
	}
	if caCert.Leaf, err = x509.ParseCertificate(caCert.Certificate[0]); err != nil {
		return nil, err
	}
	tlsCfg := goproxy.TLSConfigFromCA(&caCert)

	s := &Server{
		proxy:  goproxy.NewProxyHttpServer(),
		log:    slog.Default(),
		mitm:   &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: tlsCfg},
		tunnel: &goproxy.ConnectAction{Action: goproxy.ConnectAccept, TLSConfig: tlsCfg},
		reject: &goproxy.ConnectAction{Action: goproxy.ConnectReject, TLSConfig: tlsCfg},
	}
	// Every upstream connection goes through guardedDial. A request carries
	// whether its host is allowed only by "*" (set in the request handler
	// below); one that does not say is treated as wildcard-only.
	s.proxy.Tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		wildcard, ok := ctx.Value(ctxKey{}).(bool)
		return guardedDial(ctx, network, addr, wildcard || !ok)
	}
	s.proxy.ConnectDial = nil
	s.proxy.ConnectDialWithReq = func(req *http.Request, network, addr string) (net.Conn, error) {
		explicit := s.rules.Load().allowlistFor(req.RemoteAddr).Explicit(addr)
		return guardedDial(req.Context(), network, addr, !explicit)
	}
	s.UpdateSecrets(secrets)
	empty, _ := compile(RuleSet{})
	s.rules.Store(empty)
	s.proxy.Verbose = false

	// Gate CONNECT per source: MITM inject hosts, tunnel other allowed hosts,
	// reject the rest. The source IP (the sandbox's network) selects which
	// project's allowlist applies.
	s.proxy.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		src := ""
		if ctx.Req != nil {
			src = ctx.Req.RemoteAddr
		}
		c := s.rules.Load()
		if !c.allowlistFor(src).Allowed(host) {
			s.log.Info("connect", "decision", "deny", "host", host, "src", src)
			return s.reject, host
		}
		if c.mitm(host) {
			s.log.Info("connect", "decision", "allow", "mode", "mitm", "host", host, "src", src)
			return s.mitm, host
		}
		s.log.Info("connect", "decision", "allow", "mode", "tunnel", "host", host, "src", src)
		return s.tunnel, host
	})

	// Every request (plain HTTP and each request inside a MITM'd tunnel) is
	// gated here by source. A MITM'd request can name any host in its URL or
	// Host header regardless of the tunnel it arrived on, so the CONNECT gate
	// alone is not enough: re-check the allowlist on the request's own host,
	// and refuse a Host header that disagrees with the URL, so an injected
	// secret only ever goes to the host its rule names.
	s.proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		c := s.rules.Load()
		if req.Host != "" && !strings.EqualFold((&url.URL{Host: req.Host}).Hostname(), req.URL.Hostname()) {
			s.log.Info("request", "decision", "deny", "reason", "host header mismatch", "method", req.Method,
				"host", req.URL.Host, "host_header", req.Host, "src", req.RemoteAddr)
			return req, goproxy.NewResponse(req, goproxy.ContentTypeText,
				http.StatusForbidden, "aisb-proxy: Host header does not match the request URL\n")
		}
		if !c.allowlistFor(req.RemoteAddr).Allowed(req.URL.Host) {
			s.log.Info("request", "decision", "deny", "method", req.Method,
				"host", req.URL.Host, "src", req.RemoteAddr)
			return req, goproxy.NewResponse(req, goproxy.ContentTypeText,
				http.StatusForbidden, "aisb-proxy: host not in allowlist\n")
		}
		req = req.WithContext(withWildcardOnly(req.Context(),
			!c.allowlistFor(req.RemoteAddr).Explicit(req.URL.Host)))
		secrets := *s.secrets.Load()
		st, err := swapRefresh(req, c.oauth, secrets)
		if err != nil {
			return req, goproxy.NewResponse(req, goproxy.ContentTypeText,
				http.StatusBadGateway, "aisb-proxy: could not read token request\n")
		}
		if st != nil {
			ctx.UserData = st
			s.log.Info("oauth refresh", "host", req.URL.Host, "src", req.RemoteAddr)
		}
		NewInjector(c.injectRules(), secrets).Apply(req)
		return req, nil
	})

	// A token-endpoint reply to a refresh the proxy completed: keep the new
	// real tokens, give the agent placeholders.
	s.proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		st, ok := ctx.UserData.(*refreshState)
		if !ok || resp == nil {
			return resp
		}
		if tokens := rewriteTokenReply(resp, st); len(tokens) > 0 {
			s.mergeSecrets(tokens)
			s.log.Info("oauth tokens refreshed", "host", ctx.Req.URL.Host, "count", len(tokens))
			if f := s.onRefresh.Load(); f != nil {
				(*f)(tokens)
			}
		}
		return resp
	})

	return s, nil
}

// UpdateRules atomically replaces the active rule set and recompiles. The
// caller (the proxy's rule watcher) passes the full rule set rebuilt from every
// project's file, so replace is correct and lossless. Safe to call concurrently
// with in-flight requests.
func (s *Server) UpdateRules(rs RuleSet) error {
	c, err := compile(rs)
	if err != nil {
		return err
	}
	s.rules.Store(c)
	return nil
}

// UpdateSecrets atomically replaces the secret values used for injection. The
// map is copied, so the caller may reuse it. Requests already past the
// injector keep the value they read; every later request sees the new one.
func (s *Server) UpdateSecrets(secrets map[string]string) {
	cp := make(map[string]string, len(secrets))
	for k, v := range secrets {
		cp[k] = v
	}
	s.secretsMu.Lock()
	s.secrets.Store(&cp)
	s.secretsMu.Unlock()
}

// mergeSecrets sets the given secrets on top of the current ones.
func (s *Server) mergeSecrets(set map[string]string) {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	cur := *s.secrets.Load()
	cp := make(map[string]string, len(cur)+len(set))
	for k, v := range cur {
		cp[k] = v
	}
	for k, v := range set {
		cp[k] = v
	}
	s.secrets.Store(&cp)
}

// OnRefresh registers f to receive the real tokens (secret name -> value)
// each time the proxy completes an OAuth refresh, so they can be persisted.
// f must not log or print the values.
func (s *Server) OnRefresh(f func(tokens map[string]string)) { s.onRefresh.Store(&f) }

// Handler exposes the proxy as an http.Handler (used by tests and main).
func (s *Server) Handler() http.Handler { return s.proxy }

// TrustUpstreamRoots makes the proxy trust pool when verifying upstream TLS
// certs, in addition to the system roots. Production uses system roots only;
// this exists so tests can point the proxy at a self-signed httptest upstream.
func (s *Server) TrustUpstreamRoots(pool *x509.CertPool) {
	if s.proxy.Tr.TLSClientConfig == nil {
		s.proxy.Tr.TLSClientConfig = &tls.Config{}
	}
	s.proxy.Tr.TLSClientConfig.RootCAs = pool
}
