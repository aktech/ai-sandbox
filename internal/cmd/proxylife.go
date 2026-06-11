package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/aktech/ai-sandbox/internal/cfg"
	"github.com/aktech/ai-sandbox/internal/dx"
	"github.com/aktech/ai-sandbox/internal/proxy"
)

const proxyContainer = "psb-proxy"

// proxyImage returns the proxy container image, overridable for testing/CI.
func proxyImage() string {
	if v := os.Getenv("PSB_PROXY_IMAGE"); v != "" {
		return v
	}
	return "ai-sandbox-proxy:latest"
}

// networkName maps a sandbox container name to its private network name.
func networkName(container string) string {
	return "psb-net-" + strings.TrimPrefix(container, "psb-")
}

// proxyRunArgs builds `docker run -d` argv for the proxy container. It sits on
// the default bridge (egress to the internet) and is later attached to each
// sandbox's internal network with `docker network connect`. The tmpfs at
// /run/psb holds the decrypted payload in RAM only; it never reaches disk.
func proxyRunArgs(name, image string) []string {
	return []string{"run", "-d", "--name", name, "--network", "bridge",
		"--tmpfs", "/run/psb:rw,mode=1777,size=1m", image}
}

// writePayloadExecArgs builds argv to deliver the secrets payload to a running
// proxy via stdin. The proxy's --write-payload mode copies stdin to the tmpfs
// file that its serving process reads once at startup.
func writePayloadExecArgs(name string) []string {
	return []string{"exec", "-i", name, "/psb-proxy", "--write-payload"}
}

// writeRulesExecArgs builds argv to deliver an updated rule set (no secrets) to
// a running proxy via stdin. The proxy hot-swaps its allowlist.
func writeRulesExecArgs(name string) []string {
	return []string{"exec", "-i", name, "/psb-proxy", "--write-rules"}
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
		Projects: []proxy.Subnet{{CIDR: cidr, Allow: pc.EffectiveAllow()}},
	}, nil
}

// parseProxyBlock converts the raw cfg proxy block into a parsed proxy.Config
// (allow list + typed inject rules).
func parseProxyBlock(p *cfg.ProxyBlock) (*proxy.Config, error) {
	raw, err := json.Marshal(map[string]any{"allow": p.Allow, "inject": p.Inject})
	if err != nil {
		return nil, err
	}
	return proxy.ParseConfig(raw)
}

// deliverRules computes the current project's rule update and pushes it to the
// running proxy. No master password is needed (the update carries no secrets).
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
	return e.RunWithStdin(payload, writeRulesExecArgs(proxyContainer)...)
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

// ensureProxyRunning starts the proxy container if not already up and delivers
// the payload to it. If a stale proxy exists it is removed and recreated so the
// fresh payload (CA + secrets + config) takes effect. payload is built by the
// caller from the decrypted secret store.
func ensureProxyRunning(e dx.Executor, image string, payload []byte) error {
	if !dx.ContainerRunning(e, proxyContainer) {
		if dx.ContainerExists(e, proxyContainer) {
			_ = dx.Remove(e, proxyContainer)
		}
		if err := e.RunSilent(proxyRunArgs(proxyContainer, image)...); err != nil {
			return err
		}
	}
	// Deliver the payload over stdin into the proxy's tmpfs. Safe to repeat:
	// the serving process consumes and unlinks it once.
	return e.RunWithStdin(payload, writePayloadExecArgs(proxyContainer)...)
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
	_ = e.RunSilent(netConnectArgs(net, proxyContainer)...)
	return nil
}

// teardownSandboxNet disconnects the proxy and removes the sandbox's network.
func teardownSandboxNet(e dx.Executor, container string) {
	net := networkName(container)
	if networkExists(e, net) {
		_ = e.RunSilent(netDisconnectArgs(net, proxyContainer)...)
		_ = e.RunSilent("network", "rm", net)
	}
}
