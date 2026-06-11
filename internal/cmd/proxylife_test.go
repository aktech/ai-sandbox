package cmd

import (
	"reflect"
	"testing"
)

func TestNetworkName(t *testing.T) {
	if got := networkName("psb-foo"); got != "psb-net-foo" {
		t.Fatalf("networkName = %q", got)
	}
}

func TestProxyRunArgs(t *testing.T) {
	got := proxyRunArgs("psb-proxy", "ai-sandbox-proxy:latest")
	want := []string{"run", "-d", "--name", "psb-proxy", "--network", "bridge",
		"--tmpfs", "/run/psb:rw,mode=1777,size=1m", "ai-sandbox-proxy:latest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proxyRunArgs = %#v\nwant %#v", got, want)
	}
}

func TestWritePayloadExecArgs(t *testing.T) {
	got := writePayloadExecArgs("psb-proxy")
	want := []string{"exec", "-i", "psb-proxy", "/psb-proxy", "--write-payload"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("writePayloadExecArgs = %#v\nwant %#v", got, want)
	}
}

func TestInternalNetCreateArgs(t *testing.T) {
	got := internalNetCreateArgs("psb-net-foo")
	want := []string{"network", "create", "--internal", "psb-net-foo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("internalNetCreateArgs = %#v\nwant %#v", got, want)
	}
}

func TestNetConnectDisconnectArgs(t *testing.T) {
	if got := netConnectArgs("psb-net-foo", "psb-proxy"); !reflect.DeepEqual(got,
		[]string{"network", "connect", "psb-net-foo", "psb-proxy"}) {
		t.Fatalf("netConnectArgs = %#v", got)
	}
	if got := netDisconnectArgs("psb-net-foo", "psb-proxy"); !reflect.DeepEqual(got,
		[]string{"network", "disconnect", "psb-net-foo", "psb-proxy"}) {
		t.Fatalf("netDisconnectArgs = %#v", got)
	}
}
