# ai-sandbox

[![ci](https://github.com/aktech/ai-sandbox/actions/workflows/ci.yml/badge.svg)](https://github.com/aktech/ai-sandbox/actions/workflows/ci.yml)
[![image](https://github.com/aktech/ai-sandbox/actions/workflows/image.yml/badge.svg)](https://github.com/aktech/ai-sandbox/actions/workflows/image.yml)
[![release](https://github.com/aktech/ai-sandbox/actions/workflows/release.yml/badge.svg)](https://github.com/aktech/ai-sandbox/actions/workflows/release.yml)

Run AI coding agents (Claude Code, pi, etc.) in a Docker container instead
of directly on your laptop. Each project gets its own container, and the
container can only see the folders you explicitly mount in. If the agent
goes wrong, you lose the container — not your home directory.

The CLI is called `aisb`. One command spins up (or re-enters) a sandbox
for the current project and drops you into a shell where `claude` and
`pi` are already installed.

![architecture](docs/architecture.svg)

## Install

```sh
make build       # builds ./bin/aisb — put it on your $PATH
aisb build       # builds the Docker image (one-time, ~5 min)
```

You need Docker running. On macOS, [colima](https://github.com/abiosoft/colima)
works well (`colima start`).

## Use it

```sh
cd ~/dev/my-project
aisb             # first run: creates a container, drops you into zsh
                 # next runs: re-enters the same container
```

Inside the container, run `claude` or `pi` like you normally would.

| Command          | What it does                                  |
|------------------|-----------------------------------------------|
| `aisb`           | Enter the sandbox for the current project.    |
| `aisb stop`      | Stop the current project's container.         |
| `aisb rm`        | Delete the current project's container.       |
| `aisb ls`        | List all sandboxes.                           |
| `aisb status`    | Show one container's status.                  |
| `aisb build`     | Rebuild the image.                            |

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

## What's in the image

Debian slim with `claude`, `pi`, Node, Go, `uv`, `pixi`, `git`, `zsh`.
The container runs as a non-root user whose UID/GID matches your host,
so files you create inside stay writable from outside.
