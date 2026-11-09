# ai-sandbox

[![ci](https://github.com/aktech/ai-sandbox/actions/workflows/ci.yml/badge.svg)](https://github.com/aktech/ai-sandbox/actions/workflows/ci.yml)
[![image](https://github.com/aktech/ai-sandbox/actions/workflows/image.yml/badge.svg)](https://github.com/aktech/ai-sandbox/actions/workflows/image.yml)
[![release](https://github.com/aktech/ai-sandbox/actions/workflows/release.yml/badge.svg)](https://github.com/aktech/ai-sandbox/actions/workflows/release.yml)

Run AI coding agents (Claude Code, Codex, pi, etc.) in a Docker container instead
of directly on your laptop. Each project gets its own container, and the
container can only see the folders you explicitly mount in. If the agent
goes wrong, you lose the container — not your home directory.

The CLI is called `aisb`. One command spins up (or re-enters) a sandbox
for the current project and drops you into a shell where `claude`,
`codex`, and `pi` are already installed.

![architecture](docs/architecture.svg)

## Getting started

You need Docker running. On macOS, [colima](https://github.com/abiosoft/colima)
works well (`colima start`). Nothing else: you do not need to clone this
repository or install Go.

**1. Install `aisb`.** Download the binary for your machine from the
[latest release](https://github.com/aktech/ai-sandbox/releases/latest), check
its checksum, and put it on your `PATH`:

```sh
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
F=aisb-$OS-$ARCH
curl -fsSLO "https://github.com/aktech/ai-sandbox/releases/latest/download/$F.tar.gz"
curl -fsSLO "https://github.com/aktech/ai-sandbox/releases/latest/download/$F.tar.gz.sha256"
if command -v sha256sum >/dev/null; then sha256sum -c "$F.tar.gz.sha256"; else shasum -a 256 -c "$F.tar.gz.sha256"; fi   # must print: OK
tar -xzf "$F.tar.gz"
mkdir -p ~/.local/bin && install -m 0755 "$F" ~/.local/bin/aisb
aisb help                               # if "command not found": add ~/.local/bin to your PATH
```

**2. Build the sandbox image** (one-time, about 5 minutes). Everything it
needs is inside the `aisb` binary:

```sh
aisb build
```

**3. Start a sandbox** in any project:

```sh
cd ~/dev/my-project
aisb             # first run: creates a container, drops you into zsh
                 # next runs: re-enters the same container
```

**4. Optional:** choose what the sandbox can see ([What gets mounted](#what-gets-mounted))
and keep your API keys out of it ([Keeping credentials out of the sandbox](#keeping-credentials-out-of-the-sandbox)).

**Upgrading:** download the new release the same way, then run `aisb build`
again. It rebuilds the sandbox image, and the proxy image too if you use proxy
mode, so they match the new `aisb`.

**Building from source** (contributors): `make build` writes `./bin/aisb`
from a checkout; it needs Go. Everything after that is the same.

## Use it

```sh
cd ~/dev/my-project
aisb             # first run: creates a container, drops you into zsh
                 # next runs: re-enters the same container
```

Inside the container, run `claude`, `codex`, or `pi` like you normally would.

| Command          | What it does                                  |
|------------------|-----------------------------------------------|
| `aisb`           | Enter the sandbox for the current project.    |
| `aisb stop`      | Stop the current project's container.         |
| `aisb rm`        | Delete the current project's container.       |
| `aisb ls`        | List all sandboxes.                           |
| `aisb status`    | Show one container's status.                  |
| `aisb build`     | Build or rebuild the image(s).                |

## What gets mounted

By default the container only sees the project directory. Anything else
you want available — your `.gitconfig`, dotfiles, a shared library — you
list in a config file:

`~/.config/ai-sandbox/config.json`

```json
{
  "default": {
    "mounts": [
      "{{HOME}}/.gitconfig",
      "{{HOME}}/.claude/settings.json",
      "{{CWD}}"
    ]
  },
  "projects": {
    "/Users/me/dev/my-project": {
      "extra_mounts": ["~/dev/shared-lib"],
      "memory": "8g",
      "cpus": 4,
      "gpus": "all",
      "ports": ["3000:3000", "8000:8000"]
    }
  }
}
```

- `mounts` — the full list. Each entry becomes a `-v` bind mount. Write
  `src` to mount a host path at the same path inside the container, or
  `src:dest` to mount it at a different path (e.g. a repo-local gitconfig
  at `~/.gitconfig` inside the sandbox).
- `extra_mounts` — appended to `mounts`. Use for per-project additions.
- `copies` / `extra_copies`: host paths copied *into* the sandbox when its
  container is first created, using the same `src` / `src:dest` syntax as
  `mounts`. Unlike a mount, nothing the agent changes ever flows back to the
  host, so this is the safe way to pre-warm caches such as `~/.cache/pip` or
  `~/go/pkg/mod`. A directory's contents land at `dest`. Copies run in the
  background by default; `aisb --wait-copies` waits for them before opening
  the shell. They only happen on creation (`aisb rm` then `aisb` to redo
  them) and are skipped by `aisb create`.
- `memory`, `cpus`, `gpus`, `image` — optional overrides. Set them under
  `default` to apply everywhere, or under a project to scope them.
- `gpus` — passed straight to `docker run --gpus` (`"all"`, `"device=0,1"`,
  or a count like `"2"`). Omit it (the default) for no GPU. Requires the
  [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)
  on a Linux host with NVIDIA drivers — it does **not** work under colima/macOS.
- `ports` — each entry becomes a `-p host:container` publish, so a server
  you start inside the sandbox is reachable from your laptop browser. With
  `"3000:3000"`, a dev server on `:3000` inside shows up at
  `http://localhost:3000` on the host.

Two sandboxes can both serve `:3000` *inside*, but the host side of a
publish is shared: if two projects map to the same host port (e.g. both
`"3000:3000"`), only the first container binds and the second fails with
`port is already allocated`. Give each project a distinct host port for
the same container port: `"3001:3000"`, `"3002:3000"`, and so on. Ports are
fixed when the container is created, so changing them needs `aisb rm`
followed by `aisb`.

Placeholders: `{{HOME}}`, `{{CWD}}`, `{{SHARED_DIR}}`. Shell-style `~/`
and `$VAR` also work, on both sides of a `src:dest` entry. Paths that
don't exist on the host are skipped with a warning.

Projects and mounts can be any existing directory, inside or outside your home
(`/private/tmp/x`, `/Volumes/disk/x`, `/opt/x`). The host root and system
directories (`/etc`, `/usr`, `/dev`, `/proc`, and so on) are skipped with a
warning. A Docker that runs in a VM only sees the host paths that VM shares:
colima shares just your home directory by default, so to use a project
elsewhere add it to the `mounts` list in colima's config (`colima stop`, then
`colima start --edit`), keeping your home directory listed too. If you don't,
`aisb` stops with docker's "bind source path does not exist" and names the
paths to add.

## Environment overrides

| Var                 | Default                              |
|--------------------|--------------------------------------|
| `AISB_IMAGE_NAME`   | `ai-sandbox-pi:latest`               |
| `AISB_MEMORY`       | `4g`                                 |
| `AISB_CPUS`         | `2`                                  |
| `AISB_GPUS`         | none (e.g. `all` to expose all GPUs) |
| `AISB_SHARED_DIR`   | `~/sb-shared`                        |
| `AISB_CONFIG_FILE`  | `~/.config/ai-sandbox/config.json`   |
| `ANTHROPIC_API_KEY` | passed through to the container      |
| `AISB_PROXY_IMAGE`  | `ai-sandbox-proxy:latest`            |
| `AISB_PROXY_NAME`   | `aisb-proxy` for the default config dir; `aisb-proxy-<id>` for any other `AISB_CONFIG_FILE` dir |
| `AISB_MASTER_PASSWORD` | unlocks the secret store non-interactively |

## Keeping credentials out of the sandbox

By default `aisb` passes your `ANTHROPIC_API_KEY` straight into the container.
That means a misbehaving agent can read it. The optional credential proxy
removes every real secret from the sandbox: the agent sees only placeholder
values, and a separate proxy on your machine swaps in the real secret as each
request leaves.

How it works:

- Real secrets live in an encrypted file on your host
  (`~/.config/ai-sandbox/secrets.enc`), unlocked with a master password.
- A small `aisb-proxy` container holds the decrypted secrets in memory (they
  are handed to it over a pipe, never written to disk or passed as env/args).
- Each sandbox runs on its own private Docker network that has **no route to
  the internet**. The proxy is the only way out, so the domain allowlist is
  enforced by the network itself, not by the agent's good behavior.
- Inside the sandbox, the env var you name in `proxy.env` (e.g. `GH_TOKEN`) is
  set to a sentinel like `__aisb_<secret>__`. When the agent calls an
  allowlisted host that has an inject rule, the proxy replaces the sentinel in
  the header with the real secret. The agent never holds the key. Which secret
  goes in which env var and which header is entirely config-driven; nothing is
  hardcoded.

Set it up once:

**1. Initialise proxy mode.** This asks you to choose a master password (typed
twice; it cannot be recovered), creates the encrypted secret store, and builds
the proxy image from the `aisb` binary:

```sh
aisb proxy init
```

**2. Store your keys.** Each key gets a name you choose; you are asked for the
master password, then the value (or pipe the value in):

```sh
aisb secret set github                              # prompts for the value
printf '%s' "$ANTHROPIC_API_KEY" | aisb secret set anthropic
```

**3. Add a `proxy` block to your config** (`~/.config/ai-sandbox/config.json`)
saying which hosts sandboxes may reach and where each key goes:

```json
{
  "default": {
    "mounts": ["{{CWD}}"],
    "proxy": {
      "allow": ["registry.npmjs.org", "pypi.org", "files.pythonhosted.org"],
      "env": { "github": "GH_TOKEN", "anthropic": "ANTHROPIC_API_KEY" },
      "inject": {
        "api.github.com":    {"header": "Authorization", "secret": "github", "format": "Bearer %s"},
        "github.com":        {"header": "Authorization", "secret": "github", "basic": true},
        "api.anthropic.com": {"header": "x-api-key", "secret": "anthropic"}
      }
    }
  },
  "projects": {
    "/Users/me/dev/my-project": { "extra_allow": ["files.pythonhosted.org"] },
    "/Users/me/dev/scratch":    { "proxy": { "allow": ["*"] } }
  }
}
```

- `env` maps each secret name to the environment variable the sandbox sets to
  that secret's sentinel, so a CLI inside believes it is authenticated. Pick
  whatever names your tools read (`GH_TOKEN`, `ANTHROPIC_API_KEY`, etc.).
- `allow` is default-deny: only listed hosts are reachable. `*.` matches any
  subdomain depth; a plain entry matches that exact host. A host you inject
  into is allowed automatically.
- Entries are port-aware. `host` reaches ports 80 and 443 only, `host:5432`
  reaches that one port, and `host:*` reaches any port. Write IPv6 addresses
  bare (`2001:db8::1`) or bracketed with a port (`[2001:db8::1]:8443`).
- `inject` says which header to rewrite per host. `format` wraps the value
  (e.g. `Bearer %s`); `basic` handles `git push` over HTTPS, which sends the
  token as the password half of a Basic-auth header.
- `default.proxy.allow` is the **universal** list, applied to every project.

**4. Run `aisb` in a project.** It starts the proxy and asks for the master
password once (it stays running until `aisb proxy stop`; set
`AISB_MASTER_PASSWORD` to skip the prompt). If anything is missing, `aisb`
says exactly which command to run. To turn proxy mode off again, remove the
`proxy` block.

### Per-project allowlists

The single shared proxy enforces a **different allowlist per project**. It tells
which project a request came from by the sandbox's own network, so each
project's rules are isolated from the others.

- `extra_allow` (per project) adds hosts on top of the universal list for that
  project only.
- A per-project `"proxy": { "allow": [...] }` block adds more hosts for that
  project. Use `"allow": ["*"]` to make a project **unrestricted** (reach any
  host on the internet). `*` does not cover addresses on your own machine or
  network: loopback, private ranges (10/8, 172.16/12, 192.168/16, 100.64/10,
  fc00::/7), link-local (including cloud metadata at 169.254.169.254) and other
  sandboxes stay unreachable, whether named by IP or by a name that resolves
  there. List such a host explicitly (e.g. `"nas.lan:8080"`) to reach it.
  Even an unrestricted project still has its secrets injected and kept out of
  the container; only the egress limits are lifted.

Secrets update live too: `aisb secret set` and `aisb secret rm` hand the new
values to a running proxy, so a refreshed token is used from the next request
without restarting anything.

### OAuth logins (Codex, Claude Code)

Agents signed in with OAuth refresh their access token themselves, and the
reply carries a new access token and often a new refresh token. The `oauth`
block lets the proxy complete those refreshes so neither real token ever enters
the sandbox:

```json
"proxy": {
  "inject": {
    "chatgpt.com":       {"header": "Authorization", "secret": "codex",  "format": "Bearer %s"},
    "api.anthropic.com": {"header": "Authorization", "secret": "claude", "format": "Bearer %s"}
  },
  "oauth": {
    "auth.openai.com":     {"path": "/oauth/token",    "access": "codex",  "refresh": "codex_refresh"},
    "platform.claude.com": {"path": "/v1/oauth/token", "access": "claude", "refresh": "claude_refresh"}
  }
}
```

Store the real tokens with `aisb secret set codex` / `codex_refresh` (and the
same for `claude`), and give the agent a credentials file holding the
placeholders `__aisb_codex__` and `__aisb_codex_refresh__` in their place.
When the agent posts a refresh carrying the placeholder refresh token (JSON or
form body), the proxy sends the real one, keeps the new real tokens for the
following requests, and replies with placeholders: a JWT keeps its header and
claims (so the agent can still read its expiry) with the signature replaced,
and a token like `sk-ant-oat01-...` keeps its `sk-ant-oat01-` prefix. Real
token text anywhere in the reply, including error replies, is masked, and a
reply the proxy cannot read is refused.

The endpoints above are the ones the Codex CLI (`auth.openai.com/oauth/token`,
JSON body) and Claude Code (`platform.claude.com/v1/oauth/token`, JSON body)
use at the time of writing.

New tokens live in the proxy's memory and in an owner-only file on its RAM
disk until a host command that unlocks your store saves them: `aisb secret
set`, `aisb secret rm`, `aisb secret sync` or `aisb proxy stop` (which saves
them before stopping, so a rotated refresh token is not lost).

Rules update live: entering a project pushes its allowlist to the running proxy
(no master password needed, since rule updates carry no secrets). Removing a
sandbox (`aisb rm`) drops that project's rules.

Once a `proxy` block is present, `aisb` starts the proxy and the private
network automatically. With no `proxy` block, `aisb` behaves exactly as before.

| Command            | What it does                                   |
|--------------------|------------------------------------------------|
| `aisb proxy init`   | One-time setup: password, store, proxy image.  |
| `aisb secret set X` | Store a secret (value read from stdin/no-echo).|
| `aisb secret ls`    | List stored secret names (never values).       |
| `aisb secret rm X`  | Remove a secret.                               |
| `aisb secret sync`  | Save tokens the proxy refreshed into the store.|
| `aisb proxy stop`   | Stop the shared proxy container.               |
| `aisb proxy log`    | Stream the proxy's allow/deny decisions.       |

In proxy mode `aisb` also refuses to mount or copy host credentials into the
sandbox, warning about each: `~/.ssh`, cloud and cluster logins (`~/.aws`,
`~/.config/gcloud`, `~/.kube`, ...), registry and package tokens (`~/.docker`,
`~/.npmrc`, `~/.netrc`, `~/.config/gh`, ...), agent login files (Codex
`auth.json`, Claude Code credentials, pi `auth.json`), container runtime
directories and any socket (a Docker socket is control of the host), aisb's own
config directory (CA key, secret store), and anything that contains one of
these, such as your whole home directory.

Git over SSH (`git@github.com:...`) is out of scope for now: use HTTPS
remotes so the proxy can inject your token. Projects that publish `ports`
also need an inbound forwarder that is not yet built, so `ports` and proxy
mode do not yet combine.

## What's in the image

Debian slim with `claude`, `codex`, `pi`, Node, Go, `uv`, `pixi`, `git`, `zsh`.
The container runs as a non-root user whose UID/GID matches your host,
so files you create inside stay writable from outside.

### Keeping the agents up to date

The image is a snapshot: what is installed changes only when you run
`aisb build`, and a sandbox keeps the image it was created from until you
`aisb rm` it and run `aisb` again.

- **Claude Code** updates itself inside each sandbox in the background, so
  it stays current without a rebuild.
- **pi** and **Codex** are pinned to exact versions in the Dockerfile. Each
  `aisb` release moves those pins forward; after upgrading `aisb`, run
  `aisb build`, then `aisb rm` and `aisb` in a project to get a sandbox
  with the new versions.
- To try a newer version before a release picks it up:
  `PI_VERSION=v1.5.0 CODEX_VERSION=0.165.0 aisb build`. pi downloads are
  checked against the release's `SHA256SUMS`.
