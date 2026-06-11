package cmd

import (
	"os"
	"strings"

	"github.com/aktech/ai-sandbox/internal/dx"
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

// writePayloadExecArgs builds argv to deliver the payload to a running proxy
// via stdin. The proxy's --write-payload mode copies stdin to the tmpfs file
// that its serving process is polling for.
func writePayloadExecArgs(name string) []string {
	return []string{"exec", "-i", name, "/psb-proxy", "--write-payload"}
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
