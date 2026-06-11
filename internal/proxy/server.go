package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/elazarl/goproxy"
)

// hostOnly strips an optional ":port" suffix.
func hostOnly(host string) string {
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}

type Server struct {
	proxy *goproxy.ProxyHttpServer
}

// New builds a proxy that allows only EffectiveAllow() hosts, MITMs them so
// it can inject secrets, and logs each decision. caCertPEM/caKeyPEM sign the
// per-host leaf certs goproxy mints on the fly.
func New(cfg *Config, secrets map[string]string, caCertPEM, caKeyPEM []byte) (*Server, error) {
	caCert, err := tls.X509KeyPair(caCertPEM, caKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("load CA keypair: %w", err)
	}
	if caCert.Leaf, err = x509.ParseCertificate(caCert.Certificate[0]); err != nil {
		return nil, err
	}

	// Per-server connect actions signed by our CA. Built locally rather than
	// mutating goproxy's package globals so concurrent servers/tests don't race.
	tlsCfg := goproxy.TLSConfigFromCA(&caCert)
	mitm := &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: tlsCfg}
	tunnel := &goproxy.ConnectAction{Action: goproxy.ConnectAccept, TLSConfig: tlsCfg}
	reject := &goproxy.ConnectAction{Action: goproxy.ConnectReject, TLSConfig: tlsCfg}

	allow := NewAllowlist(cfg.EffectiveAllow())
	inj := NewInjector(cfg.Inject, secrets)

	// injectHosts is the set of hosts we must MITM to rewrite a header. Every
	// other allowlisted host is tunneled end-to-end so the proxy never sees the
	// plaintext (e.g. a Claude subscription OAuth token stays private).
	injectHosts := map[string]bool{}
	for h := range cfg.Inject {
		injectHosts[h] = true
	}

	p := goproxy.NewProxyHttpServer()
	p.Verbose = false

	// Gate CONNECT: MITM hosts that need injection, tunnel other allowlisted
	// hosts untouched, reject everything else.
	p.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		if !allow.Allowed(host) {
			log.Printf("DENY CONNECT %s", host)
			return reject, host
		}
		if injectHosts[hostOnly(host)] {
			log.Printf("ALLOW CONNECT %s (mitm: inject)", host)
			return mitm, host
		}
		log.Printf("ALLOW CONNECT %s (tunnel)", host)
		return tunnel, host
	})

	// Gate plain HTTP and inject on the (now decrypted) request.
	p.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		if !allow.Allowed(req.URL.Host) {
			log.Printf("DENY %s %s", req.Method, req.URL.Host)
			return req, goproxy.NewResponse(req, goproxy.ContentTypeText,
				http.StatusForbidden, "psb-proxy: host not in allowlist\n")
		}
		inj.Apply(req)
		log.Printf("ALLOW %s %s", req.Method, req.URL.Host)
		return req, nil
	})

	return &Server{proxy: p}, nil
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
