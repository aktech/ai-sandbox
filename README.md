# ai-sandbox

[![ci](https://github.com/aktech/ai-sandbox/actions/workflows/ci.yml/badge.svg)](https://github.com/aktech/ai-sandbox/actions/workflows/ci.yml)
[![image](https://github.com/aktech/ai-sandbox/actions/workflows/image.yml/badge.svg)](https://github.com/aktech/ai-sandbox/actions/workflows/image.yml)
[![release](https://github.com/aktech/ai-sandbox/actions/workflows/release.yml/badge.svg)](https://github.com/aktech/ai-sandbox/actions/workflows/release.yml)

Run AI coding agents (Claude Code, pi, etc.) in a Docker container instead
of directly on your laptop. Each project gets its own container, and the
container can only see the folders you explicitly mount in. If the agent
goes wrong, you lose the container — not your home directory.

The CLI is called `psb`. One command spins up (or re-enters) a sandbox
for the current project and drops you into a shell where `claude` and
`pi` are already installed.

![architecture](docs/architecture.svg)

## Install

```sh
make build       # builds ./bin/psb — put it on your $PATH
psb build        # builds the Docker image (one-time, ~5 min)
```

You need Docker running. On macOS, [colima](https://github.com/abiosoft/colima)
works well (`colima start`).

## Use it

```sh
cd ~/dev/my-project
psb              # first run: creates a container, drops you into zsh
                 # next runs: re-enters the same container
```

Inside the container, run `claude` or `pi` like you normally would.

| Command          | What it does                                  |
|------------------|-----------------------------------------------|
| `psb`            | Enter the sandbox for the current project.    |
| `psb stop`       | Stop the current project's container.         |
| `psb rm`         | Delete the current project's container.       |
| `psb ls`         | List all sandboxes.                           |
| `psb status`     | Show one container's status.                  |
| `psb build`      | Rebuild the image.                            |

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
- `memory`, `cpus`, `image` — optional per-project overrides.
- `ports` — each entry becomes a `-p host:container` publish, so a server
  you start inside the sandbox is reachable from your laptop browser. With
  `"3000:3000"`, a dev server on `:3000` inside shows up at
  `http://localhost:3000` on the host.

Two sandboxes can both serve `:3000` *inside*, but the host side of a
publish is shared: if two projects map to the same host port (e.g. both
`"3000:3000"`), only the first container binds and the second fails with
`port is already allocated`. Give each project a distinct host port for
the same container port: `"3001:3000"`, `"3002:3000"`, and so on. Ports are
fixed when the container is created, so changing them needs `psb rm`
followed by `psb`.

Placeholders: `{{HOME}}`, `{{CWD}}`, `{{SHARED_DIR}}`. Shell-style `~/`
and `$VAR` also work, on both sides of a `src:dest` entry. Paths that
don't exist on the host are skipped with a warning.

## Environment overrides

| Var                | Default                              |
|--------------------|--------------------------------------|
| `PSB_IMAGE_NAME`   | `ai-sandbox-pi:latest`               |
| `PSB_MEMORY`       | `4g`                                 |
| `PSB_CPUS`         | `2`                                  |
| `PSB_SHARED_DIR`   | `~/sb-shared`                        |
| `PSB_CONFIG_FILE`  | `~/.config/ai-sandbox/config.json`   |
| `ANTHROPIC_API_KEY`| passed through to the container      |
| `PSB_PROXY_IMAGE`  | `ai-sandbox-proxy:latest`            |
| `PSB_MASTER_PASSWORD` | unlocks the secret store non-interactively |

## Keeping credentials out of the sandbox

By default `psb` passes your `ANTHROPIC_API_KEY` straight into the container.
That means a misbehaving agent can read it. The optional credential proxy
removes every real secret from the sandbox: the agent sees only placeholder
values, and a separate proxy on your machine swaps in the real secret as each
request leaves.

How it works:

- Real secrets live in an encrypted file on your host
  (`~/.config/ai-sandbox/secrets.enc`), unlocked with a master password.
- A small `psb-proxy` container holds the decrypted secrets in memory (they
  are handed to it over a pipe, never written to disk or passed as env/args).
- Each sandbox runs on its own private Docker network that has **no route to
  the internet**. The proxy is the only way out, so the domain allowlist is
  enforced by the network itself, not by the agent's good behavior.
- Inside the sandbox, the env var you name in `proxy.env` (e.g. `GH_TOKEN`) is
  set to a sentinel like `__psb_<secret>__`. When the agent calls an
  allowlisted host that has an inject rule, the proxy replaces the sentinel in
  the header with the real secret. The agent never holds the key. Which secret
  goes in which env var and which header is entirely config-driven; nothing is
  hardcoded.

Set it up once (the secret names below are arbitrary labels you choose):

```sh
psb build                      # if you haven't already
make proxy-image               # build the proxy container (one-time)
psb proxy init                 # generate the CA + empty secret store
printf '%s' "$SOME_API_KEY" | psb secret set <name>
```

Then add a `proxy` block to your config:

```json
{
  "default": {
    "mounts": ["{{CWD}}"],
    "proxy": {
      "allow": ["api.example.com", "registry.npmjs.org", "pypi.org"],
      "env": { "myservice": "MYSERVICE_TOKEN" },
      "inject": {
        "api.example.com": {"header": "Authorization", "secret": "myservice", "format": "Bearer %s"},
        "api.example.com": {"header": "x-api-key", "secret": "myservice"},
        "example.com":     {"header": "Authorization", "secret": "myservice", "basic": true}
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
- `inject` says which header to rewrite per host. `format` wraps the value
  (e.g. `Bearer %s`); `basic` handles `git push` over HTTPS, which sends the
  token as the password half of a Basic-auth header.
- `default.proxy.allow` is the **universal** list, applied to every project.

### Per-project allowlists

The single shared proxy enforces a **different allowlist per project**. It tells
which project a request came from by the sandbox's own network, so each
project's rules are isolated from the others.

- `extra_allow` (per project) adds hosts on top of the universal list for that
  project only.
- A per-project `"proxy": { "allow": [...] }` block adds more hosts for that
  project. Use `"allow": ["*"]` to make a project **unrestricted** (reach any
  host). Even an unrestricted project still has its secrets injected and kept
  out of the container; only the egress limits are lifted.

Rules update live: entering a project pushes its allowlist to the running proxy
(no master password needed, since rule updates carry no secrets). Removing a
sandbox (`psb rm`) drops that project's rules.

Once a `proxy` block is present, `psb` starts the proxy and the private
network automatically. With no `proxy` block, `psb` behaves exactly as before.

| Command            | What it does                                   |
|--------------------|------------------------------------------------|
| `psb proxy init`   | Generate the CA and an empty secret store.     |
| `psb secret set X` | Store a secret (value read from stdin/no-echo).|
| `psb secret ls`    | List stored secret names (never values).       |
| `psb secret rm X`  | Remove a secret.                               |
| `psb proxy stop`   | Stop the shared proxy container.               |
| `psb proxy log`    | Stream the proxy's allow/deny decisions.       |

Git over SSH (`git@github.com:...`) is out of scope for now: use HTTPS
remotes so the proxy can inject your token. Projects that publish `ports`
also need an inbound forwarder that is not yet built, so `ports` and proxy
mode do not yet combine.

## What's in the image

Debian slim with `claude`, `pi`, Node, Go, `uv`, `pixi`, `git`, `zsh`.
The container runs as a non-root user whose UID/GID matches your host,
so files you create inside stay writable from outside.
