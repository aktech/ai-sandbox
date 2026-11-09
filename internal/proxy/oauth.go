package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/elazarl/goproxy"
)

// OAuthRule makes the proxy handle an OAuth token endpoint (RFC 6749 refresh
// grant) so the agent never holds a real access or refresh token. Keyed by
// host in RuleSet.OAuth. On an HTTPS POST to Path whose refresh_token is
// the placeholder of the Refresh secret, the proxy sends the real refresh
// token, keeps the access_token and refresh_token from the reply as the new
// Access and Refresh secrets, and hands the agent placeholders instead.
//
// Verified shapes: Codex CLI posts JSON to https://auth.openai.com/oauth/token;
// Claude Code posts JSON to https://platform.claude.com/v1/oauth/token. Both
// read access_token, refresh_token (and Codex id_token) from a JSON reply.
type OAuthRule struct {
	Path    string `json:"path"`
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
}

// maxTokenBody caps a token request or reply the proxy reads into memory.
const maxTokenBody = 1 << 20

// refreshState travels from the request handler to the response handler.
type refreshState struct {
	rule        OAuthRule
	realRefresh string
}

// swapRefresh replaces the placeholder refresh token in a token-endpoint
// request body with the real one. It reports what the reply handler needs,
// or nil when the request is not a refresh carrying the placeholder.
func swapRefresh(req *http.Request, rules map[string]OAuthRule, secrets map[string]string) (*refreshState, error) {
	rule, ok := rules[req.URL.Hostname()]
	if !ok || req.URL.Scheme != "https" || req.Method != http.MethodPost || req.URL.Path != rule.Path {
		return nil, nil
	}
	real, ok := secrets[rule.Refresh]
	if !ok || req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxTokenBody+1))
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	setBody := func(b []byte) {
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.ContentLength = int64(len(b))
		req.Header.Set("Content-Length", strconv.Itoa(len(b)))
	}
	setBody(body)
	if len(body) > maxTokenBody {
		return nil, nil
	}
	sentinel := Sentinel(rule.Refresh)
	mt, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	var out []byte
	switch mt {
	case "application/json":
		var m map[string]any
		if json.Unmarshal(body, &m) != nil {
			return nil, nil
		}
		if rt, _ := m["refresh_token"].(string); !strings.Contains(rt, sentinel) {
			return nil, nil
		}
		m["refresh_token"] = real
		if out, err = json.Marshal(m); err != nil {
			return nil, err
		}
	case "application/x-www-form-urlencoded":
		v, err := url.ParseQuery(string(body))
		if err != nil || !strings.Contains(v.Get("refresh_token"), sentinel) {
			return nil, nil
		}
		v.Set("refresh_token", real)
		out = []byte(v.Encode())
	default:
		return nil, nil
	}
	setBody(out)
	// The reply must come back readable to be rewritten: no compression.
	req.Header.Del("Accept-Encoding")
	return &refreshState{rule: rule, realRefresh: real}, nil
}

// rewriteTokenReply replaces real tokens in a token-endpoint reply with
// placeholders and returns the new real tokens (secret name -> value). A
// success reply it cannot read is replaced by a 502, so a real token never
// reaches the agent; any real token text left in any reply is masked.
func rewriteTokenReply(resp *http.Response, st *refreshState) map[string]string {
	fail := func() map[string]string {
		resp.Body.Close()
		*resp = *goproxy.NewResponse(resp.Request, goproxy.ContentTypeText, http.StatusBadGateway,
			"aisb-proxy: unreadable token endpoint reply\n")
		return nil
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return fail()
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody+1))
	resp.Body.Close()
	if err != nil || len(body) > maxTokenBody {
		return fail()
	}
	mask := map[string]string{st.realRefresh: Sentinel(st.rule.Refresh)}
	var captured map[string]string
	if resp.StatusCode/100 == 2 {
		var m map[string]any
		if json.Unmarshal(body, &m) != nil {
			return fail()
		}
		captured = map[string]string{}
		for field, name := range map[string]string{
			"access_token":  st.rule.Access,
			"refresh_token": st.rule.Refresh,
			"id_token":      st.rule.Access + "_id_token",
		} {
			v, ok := m[field].(string)
			if !ok || v == "" {
				continue
			}
			ph := placeholder(v, Sentinel(name))
			m[field] = ph
			mask[v] = ph
			if strings.HasSuffix(ph, "."+Sentinel(name)) {
				mask[jwtSig(v)] = Sentinel(name) // a JWT's signature on its own
			}
			if field != "id_token" { // id_token is never injected; only its claims matter
				captured[name] = v
			}
		}
		if body, err = json.Marshal(m); err != nil {
			return fail()
		}
	}
	for real, ph := range mask {
		if real != "" {
			body = bytes.ReplaceAll(body, []byte(real), []byte(ph))
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Del("Transfer-Encoding")
	return captured
}

// placeholder returns a stand-in for real that is shaped like it. A JWT
// keeps its header and claims (not secret on their own) with the signature
// replaced by sentinel. A token with a short prefix like "sk-ant-oat01-"
// keeps the prefix. Anything else becomes the bare sentinel.
func placeholder(real, sentinel string) string {
	if p := strings.Split(real, "."); len(p) == 3 && jsonSegment(p[0]) && jsonSegment(p[1]) {
		return p[0] + "." + p[1] + "." + sentinel
	}
	segs := strings.Split(real, "-")
	prefix := ""
	for i := 0; i < len(segs)-1 && i < 3 && shortWord(segs[i]); i++ {
		prefix += segs[i] + "-"
	}
	return prefix + sentinel
}

// jwtSig returns the signature segment of a JWT ("" if real is not one).
func jwtSig(real string) string {
	if p := strings.Split(real, "."); len(p) == 3 {
		return p[2]
	}
	return ""
}

func jsonSegment(s string) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && json.Valid(b) && len(b) > 0 && b[0] == '{'
}

func shortWord(s string) bool {
	if s == "" || len(s) > 6 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
