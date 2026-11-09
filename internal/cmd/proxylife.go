package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aktech/ai-sandbox/internal/cfg"
	"github.com/aktech/ai-sandbox/internal/dx"
	"github.com/aktech/ai-sandbox/internal/proxy"
)

// proxyName is the shared proxy's container name, which is also the host name
// sandboxes reach it by. AISB_PROXY_NAME sets it. Otherwise the default config
// directory keeps the historical "aisb-proxy", and any other config directory
// (AISB_CONFIG_FILE elsewhere, i.e. a separate CA and secret store) gets
// "aisb-proxy-<id>" derived from that directory, so two setups never share,
// stop or reconfigure one another's proxy.
func proxyName() string {
	if v := os.Getenv("AISB_PROXY_NAME"); v != "" {
		return v
	}
	dir := filepath.Clean(configDir())
	if dir == filepath.Clean(defaultConfigDir()) {
		return "aisb-proxy"
	}
	sum := sha256.Sum256([]byte(dir))
	return "aisb-proxy-" + hex.EncodeToString(sum[:])[:8]
}

// ProxyImage returns the proxy container image, overridable for testing/CI.
func ProxyImage() string {
	if v := os.Getenv("AISB_PROXY_IMAGE"); v != "" {
		return v
	}
	return "ai-sandbox-proxy:latest"
}

// networkName maps a sandbox container name to its private network name.
func networkName(container string) string {
	return "aisb-net-" + strings.TrimPrefix(container, "aisb-")
}

// proxyRunArgs builds `docker run -d` argv for the proxy container. It sits on
// the default bridge (egress to the internet) and is later attached to each
// sandbox's internal network with `docker network connect`. The tmpfs at
// /run/aisb holds the decrypted payload in RAM only; it never reaches disk.
func proxyRunArgs(name, image string) []string {
	return []string{"run", "-d", "--name", name, "--network", "bridge",
		"--tmpfs", "/run/aisb:rw,mode=1777,size=1m", image}
}

// writePayloadExecArgs builds argv to deliver the secrets payload to a running
// proxy via stdin. The proxy's --write-payload mode copies stdin to the tmpfs
// file that its serving process reads once at startup.
func writePayloadExecArgs(name string) []string {
	return []string{"exec", "-i", name, "/aisb-proxy", "--write-payload"}
}

// writeSecretsExecArgs builds argv to hand a running proxy new secret values
// (no CA) via stdin. The proxy watches the file this writes and swaps the
// values in for its next request.
func writeSecretsExecArgs(name string) []string {
	return []string{"exec", "-i", name, "/aisb-proxy", "--write-secrets"}
}

// writeRulesExecArgs builds argv to deliver one project's rule file (no
// secrets) to a running proxy via stdin. key names the per-project file, so
// concurrent aisb runs never clobber each other's rules.
func writeRulesExecArgs(proxyName, key string) []string {
	return []string{"exec", "-i", proxyName, "/aisb-proxy", "--write-rules", key}
}

// rmRulesExecArgs builds argv to drop a project's rule file when its sandbox is
// removed, so the proxy stops carrying that subnet's allowances.
func rmRulesExecArgs(proxyName, key string) []string {
	return []string{"exec", proxyName, "/aisb-proxy", "--rm-rules", key}
}

// networkSubnet returns the CIDR of a docker network, e.g. "172.20.0.0/16".
func networkSubnet(e dx.Executor, net string) (string, error) {
	out, err := e.Output("network", "inspect", net,
		"--format", "{{range .IPAM.Config}}{{.Subnet}}{{end}}")
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("network %s has no subnet", net)
	}
	return out, nil
}

// rulesForProject builds the rule update a single project's sandbox delivers:
// a global inject map plus one subnet entry mapping that sandbox's network to
// the project's full allowlist. The project's allow already includes the
// default allow (cfg merges them), so Universal is left empty; an entry of "*"
// makes the project unrestricted.
func rulesForProject(c cfg.Effective, cidr string) (proxy.RuleSet, error) {
	pc, err := parseProxyBlock(c.Proxy)
	if err != nil {
		return proxy.RuleSet{}, err
	}
	return proxy.RuleSet{
		Inject:   pc.Inject,
		OAuth:    pc.OAuth,
		Projects: []proxy.Subnet{{CIDR: cidr, Allow: pc.EffectiveAllow()}},
	}, nil
}

// parseProxyBlock converts the raw cfg proxy block into a parsed proxy.Config
// (allow list + typed inject rules).
func parseProxyBlock(p *cfg.ProxyBlock) (*proxy.Config, error) {
	raw, err := json.Marshal(map[string]any{"allow": p.Allow, "inject": p.Inject, "oauth": p.OAuth})
	if err != nil {
		return nil, err
	}
	return proxy.ParseConfig(raw)
}

// deliverRules computes the current project's rule update and pushes it to the
// running proxy as that project's own rule file. No master password is needed
// (the update carries no secrets).
func deliverRules(e dx.Executor, c cfg.Effective, container string) error {
	cidr, err := networkSubnet(e, networkName(container))
	if err != nil {
		return err
	}
	rs, err := rulesForProject(c, cidr)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(rs)
	if err != nil {
		return err
	}
	return e.RunWithStdin(payload, writeRulesExecArgs(proxyName(), container)...)
}

// internalNetCreateArgs builds argv to create a sandbox's --internal network.
func internalNetCreateArgs(net string) []string {
	return []string{"network", "create", "--internal", net}
}

// netConnectArgs / netDisconnectArgs attach/detach the proxy to a sandbox net.
func netConnectArgs(net, container string) []string {
	return []string{"network", "connect", net, container}
}
func netDisconnectArgs(net, container string) []string {
	return []string{"network", "disconnect", net, container}
}

// networkExists reports whether a docker network of the given name exists.
func networkExists(e dx.Executor, net string) bool {
	out, err := e.Output("network", "ls", "--format", "{{.Name}}")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(out, "\n") {
		if l == net {
			return true
		}
	}
	return false
}

// ProxySourceLabel is the image label recording which proxy source an image
// was built from, so aisb can tell when the image is out of date.
const ProxySourceLabel = "aisb.proxy-source"

// checkProxyImage confirms the proxy image exists and was built from the proxy
// source this aisb carries (want). It runs before the master password prompt
// so a setup problem is reported first, with the command that fixes it.
func checkProxyImage(e dx.Executor, image, want string) error {
	out, err := e.Output("image", "inspect", "--format", "{{index .Config.Labels \""+ProxySourceLabel+"\"}}", image)
	if err != nil {
		return fmt.Errorf("proxy image %s not found: run `aisb build` to build it", image)
	}
	if strings.TrimSpace(out) != want {
		return fmt.Errorf("proxy image %s is out of date (built by a different aisb version): "+
			"run `aisb build` to rebuild it", image)
	}
	return nil
}

// ensureProxyRunning starts the proxy container if not already up and delivers
// the payload to it. If a stale proxy exists it is removed and recreated so the
// fresh payload (CA + secrets + config) takes effect. payload is built by the
// caller from the decrypted secret store.
func ensureProxyRunning(e dx.Executor, image string, payload []byte) error {
	if !dx.ContainerRunning(e, proxyName()) {
		if dx.ContainerExists(e, proxyName()) {
			_ = dx.Remove(e, proxyName())
		}
		if err := e.RunSilent(proxyRunArgs(proxyName(), image)...); err != nil {
			return err
		}
	}
	// Deliver the payload over stdin into the proxy's tmpfs. Safe to repeat:
	// the serving process consumes and unlinks it once.
	return e.RunWithStdin(payload, writePayloadExecArgs(proxyName())...)
}

// setupSandboxNet creates the sandbox's internal network (idempotent) and
// connects the proxy to it so the sandbox can reach the proxy but nothing else.
func setupSandboxNet(e dx.Executor, container string) error {
	net := networkName(container)
	if !networkExists(e, net) {
		if err := e.RunSilent(internalNetCreateArgs(net)...); err != nil {
			return err
		}
	}
	// Connecting an already-connected proxy returns an error we can ignore.
	_ = e.RunSilent(netConnectArgs(net, proxyName())...)
	return nil
}

// teardownSandboxNet drops the project's proxy rule file, disconnects the proxy
// and removes the sandbox's network.
func teardownSandboxNet(e dx.Executor, container string) {
	if dx.ContainerRunning(e, proxyName()) {
		_ = e.RunSilent(rmRulesExecArgs(proxyName(), container)...)
	}
	net := networkName(container)
	if networkExists(e, net) {
		_ = e.RunSilent(netDisconnectArgs(net, proxyName())...)
		_ = e.RunSilent("network", "rm", net)
	}
}
