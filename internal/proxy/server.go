package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/elazarl/goproxy"
)

// hostOnly strips an optional ":port" suffix.
func hostOnly(host string) string {
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}

// Server is the credential-injecting egress proxy. It holds the secret values
// fixed for its lifetime (set at construction) and a rule set that can be
// hot-swapped at runtime via UpdateRules, so per-project allowlists can change
// as sandboxes come and go without restarting the proxy or re-reading secrets.
type Server struct {
	proxy   *goproxy.ProxyHttpServer
	secrets map[string]string
	rules   atomic.Pointer[compiledRules]

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
		proxy:   goproxy.NewProxyHttpServer(),
		secrets: secrets,
		mitm:    &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: tlsCfg},
		tunnel:  &goproxy.ConnectAction{Action: goproxy.ConnectAccept, TLSConfig: tlsCfg},
		reject:  &goproxy.ConnectAction{Action: goproxy.ConnectReject, TLSConfig: tlsCfg},
	}
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
			log.Printf("DENY CONNECT %s (from %s)", host, src)
			return s.reject, host
		}
		if _, inject := c.injectRules()[hostOnly(host)]; inject {
			log.Printf("ALLOW CONNECT %s (mitm: inject)", host)
			return s.mitm, host
		}
		log.Printf("ALLOW CONNECT %s (tunnel)", host)
		return s.tunnel, host
	})

	// Plain HTTP (no CONNECT) is gated here by source. Injection runs for both
	// plain HTTP and MITM'd HTTPS; for HTTPS the CONNECT gate already enforced
	// the allowlist, so we do not re-deny here (the MITM'd request carries no
	// usable source address).
	s.proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		c := s.rules.Load()
		if req.URL.Scheme == "http" {
			if !c.allowlistFor(req.RemoteAddr).Allowed(req.URL.Host) {
				log.Printf("DENY %s %s (from %s)", req.Method, req.URL.Host, req.RemoteAddr)
				return req, goproxy.NewResponse(req, goproxy.ContentTypeText,
					http.StatusForbidden, "psb-proxy: host not in allowlist\n")
			}
		}
		NewInjector(c.injectRules(), s.secrets).Apply(req)
		return req, nil
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
