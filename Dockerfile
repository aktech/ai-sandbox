# Base image pinned by digest (multi-arch index). The tag is informational;
# Docker pulls exactly the digest. Digest from the Docker Hub registry API for
# library/debian:bookworm-20261104-slim (also bookworm-slim at the time).
# Bump: scripts/pins.sh image debian:bookworm-YYYYMMDD-slim
FROM debian:bookworm-20261104-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc

# Every downloaded artifact is pinned in this file, next to the step that
# uses it, by exact version plus SHA-256 (or commit SHA / image digest), and
# the build fails on any mismatch. Change pins with scripts/pins.sh; see
# "Bumping pinned versions" in the README.
#
# Oh My Zsh is cloned at this exact commit.
ARG OMZ_SHA=e64912e0c1eaa32181c3b5e5e4bf8042ecd0e8a7
ARG TARGETARCH
# Match host uid/gid so bind-mounted files stay writable. build.sh passes the
# real values from `id -u` / `id -g` / $HOME; the defaults below are only used
# when invoking `docker build` directly without those args.
ARG AGENT_UID=1000
ARG AGENT_GID=1000
ARG AGENT_HOME=/home/agent

ENV DEBIAN_FRONTEND=noninteractive

# mise (jdx/mise) lives system-wide so root-built tool installs are visible at
# runtime under the unprivileged agent user. The shims dir sits before the
# system dirs, so `node`, `go`, `python`, `uv`, `pixi` resolve to whatever is
# pinned in /etc/mise/config.toml with no per-user mise activation. It comes
# after /usr/local/bin because the agent user can write to it (see the chown
# below) and must not be able to shadow the root-owned agent binaries there.
ENV MISE_DATA_DIR=/usr/local/share/mise
ENV MISE_CONFIG_DIR=/etc/mise
ENV MISE_CACHE_DIR=/var/cache/mise
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/local/share/mise/shims:/usr/sbin:/usr/bin:/sbin:/bin

# Base tools. gnupg dropped — was only used for the (now gone) NodeSource keyring;
# zero callers remain. mise will fall back to checksum-only verification for
# tarballs whose signing it can't check, which is fine for our threat model.
RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends \
      ca-certificates curl git tar sudo \
      tini zsh tmux jq \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/* /var/log/apt/*

# aisb-fetch URL OUT SHA256: download over HTTPS and fail the build unless the
# file matches the pinned SHA-256. Every third-party download below goes
# through it. With URL "-" it only checks an existing file (used for the
# binaries unpacked from release tarballs). Removed at the end of the build.
COPY --chmod=0755 <<'SH' /usr/local/sbin/aisb-fetch
#!/bin/sh
set -eu
url=$1 out=$2 sum=$3
case "$sum" in
  *[!0-9a-f]*|'') echo "aisb-fetch: no valid sha256 pin for $url" >&2; exit 1 ;;
esac
[ "${#sum}" -eq 64 ] || { echo "aisb-fetch: sha256 pin for $url is not 64 hex chars" >&2; exit 1; }
if [ "$url" = - ]; then
  url=$out
else
  curl -fsSL --proto '=https' --tlsv1.2 --retry 5 --retry-all-errors -o "$out" "$url"
fi
echo "$sum  $out" | sha256sum --check --strict --quiet - \
  || { echo "aisb-fetch: CHECKSUM MISMATCH for $url" >&2; rm -f "$out"; exit 1; }
echo "aisb-fetch: verified sha256 $sum for $url"
SH

# mise: the release tarball from GitHub, checked against the pinned SHA-256.
# Pins come from the release's SHASUMS256.txt after verifying its GPG
# signature (SHASUMS256.asc) with the mise release key
# 24853EC9F655CE80B48E6C3A8B81C9D17413A06D (https://mise.jdx.dev/installing-mise.html).
# Bump: scripts/pins.sh mise <version>
ARG MISE_VERSION=2026.10.17
ARG MISE_PKG_SHA256_AMD64=17565e268c5542d8ce944321e17af81e0b741b65af4aa7e8e010ee77376a4aa3
ARG MISE_PKG_SHA256_ARM64=cac510e51793ad45e65fe92305bdbe4309c9c60b5907badbc7eba111738ab08f
RUN set -e \
 && ARCH="${TARGETARCH:-$(dpkg --print-architecture)}" \
 && case "$ARCH" in \
      amd64) A=x64;   SUM=$MISE_PKG_SHA256_AMD64 ;; \
      arm64) A=arm64; SUM=$MISE_PKG_SHA256_ARM64 ;; \
      *) echo "unsupported arch: $ARCH"; exit 1 ;; \
    esac \
 && aisb-fetch "https://github.com/jdx/mise/releases/download/v${MISE_VERSION}/mise-v${MISE_VERSION}-linux-${A}.tar.gz" /tmp/mise.tgz "$SUM" \
 && mkdir /tmp/mise \
 && tar -xzf /tmp/mise.tgz -C /tmp/mise --no-same-owner \
 && install -o root -g root -m 0755 /tmp/mise/mise/bin/mise /usr/local/bin/mise \
 && rm -rf /tmp/mise /tmp/mise.tgz \
 && mise --version

# Tool versions live in /etc/mise/config.toml, each an exact version. The
# lockfile next to it holds the download URL and SHA-256 of every tool for
# both Linux platforms; the install below runs with MISE_LOCKED=1, so mise
# refuses any tool the lockfile does not cover and fails on a checksum
# mismatch. Lockfile checksums were cross-checked against the publishers
# (nodejs.org SHASUMS256.txt, go.dev/dl JSON, dl.k8s.io .sha256 files, GitHub
# release asset digests). To change a version, edit config.toml and
# regenerate the lockfile: see "Bumping pinned versions" in the README.
COPY <<'TOML' /etc/mise/config.toml
[tools]
node    = "24.22.2"
go      = "1.26.10"
python  = "3.14.10"
uv      = "0.13.6"
pixi    = "0.68.0"
gh      = "2.103.3"
kubectl = "1.36.1"
k9s     = "0.50.18"
TOML

COPY <<'LOCK' /etc/mise/mise.lock
# @generated - this file is auto-generated by `mise lock` https://mise.jdx.dev/dev-tools/mise-lock.html

lockfile_version = 3

[[tools.gh]]
version = "2.103.3"
backend = "aqua:cli/cli"
specifiers = ["2.103.3"]

[tools.gh."platforms.linux-arm64"]
checksum = "sha256:ee32df2323c8847f5fac6cb6b4b69ca75828e9329a8f22f677021f7b3306ce63"
url = "https://github.com/cli/cli/releases/download/v2.103.3/gh_2.103.3_linux_arm64.tar.gz"
url_api = "https://api.github.com/repos/cli/cli/releases/assets/679195636"
provenance = "github-attestations"

[tools.gh."platforms.linux-x64"]
checksum = "sha256:4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d"
url = "https://github.com/cli/cli/releases/download/v2.103.3/gh_2.103.3_linux_amd64.tar.gz"
url_api = "https://api.github.com/repos/cli/cli/releases/assets/679195621"
provenance = "github-attestations"

[[tools.go]]
version = "1.26.10"
backend = "core:go"
specifiers = ["1.26.10"]

[tools.go."platforms.linux-arm64"]
checksum = "sha256:3f4227ce7b206a513e794f1393adb473a07b46e94d18bd15d1c684bbb7319797"
url = "https://dl.google.com/go/go1.26.10.linux-arm64.tar.gz"

[tools.go."platforms.linux-x64"]
checksum = "sha256:ccfe7ae822d2c2f41f4fe7c956199967a4dac22d0ee0d2b2bfdb440aab3b20c5"
url = "https://dl.google.com/go/go1.26.10.linux-amd64.tar.gz"

[[tools.k9s]]
version = "0.50.18"
backend = "aqua:derailed/k9s"
specifiers = ["0.50.18"]

[tools.k9s."platforms.linux-arm64"]
checksum = "sha256:d3dcc051d6be26ee911c00f583412802ebe203a189e51bc079332cb410c83b38"
url = "https://github.com/derailed/k9s/releases/download/v0.50.18/k9s_Linux_arm64.tar.gz"
url_api = "https://api.github.com/repos/derailed/k9s/releases/assets/339162564"

[tools.k9s."platforms.linux-x64"]
checksum = "sha256:0b697ed4aa80997f7de4deeed6f1fba73df191b28bf691b1f28d2f45fa2a9e9b"
url = "https://github.com/derailed/k9s/releases/download/v0.50.18/k9s_Linux_amd64.tar.gz"
url_api = "https://api.github.com/repos/derailed/k9s/releases/assets/339162570"

[[tools.kubectl]]
version = "1.36.1"
backend = "aqua:kubernetes/kubernetes/kubectl"
specifiers = ["1.36.1"]

[tools.kubectl."platforms.linux-arm64"]
checksum = "sha256:59f7ee8e477fae658447607dc3c8790ac17a1b016c01c622c12070e969e2d4e7"
url = "https://dl.k8s.io/v1.36.1/bin/linux/arm64/kubectl"

[tools.kubectl."platforms.linux-x64"]
checksum = "sha256:629d3f410e09bf49b64ae7079f7f0bda1191efed311f7d37fdbab0ad5b0ec2b7"
url = "https://dl.k8s.io/v1.36.1/bin/linux/amd64/kubectl"

[[tools.node]]
version = "24.22.2"
backend = "core:node"
specifiers = ["24.22.2"]

[tools.node."platforms.linux-arm64"]
checksum = "sha256:95c264150aceb7f3437a29fd6984924f5f8d1511ea3cbd6c508b83f6c555dae2"
url = "https://nodejs.org/dist/v24.22.2/node-v24.22.2-linux-arm64.tar.gz"

[tools.node."platforms.linux-x64"]
checksum = "sha256:361718c444e49af2ca5a110c83ba6a8901143f23ab285ebbef0341f391f482a9"
url = "https://nodejs.org/dist/v24.22.2/node-v24.22.2-linux-x64.tar.gz"

[[tools.pixi]]
version = "0.68.0"
backend = "github:prefix-dev/pixi"
specifiers = ["0.68.0"]

[tools.pixi."platforms.linux-arm64"]
checksum = "sha256:983176dcd2c9544d7a713096dc639e508df8b1a98cb2df1d1bec23be1a2afd51"
url = "https://github.com/prefix-dev/pixi/releases/download/v0.68.0/pixi-aarch64-unknown-linux-musl.tar.gz"
url_api = "https://api.github.com/repos/prefix-dev/pixi/releases/assets/415124459"

[tools.pixi."platforms.linux-x64"]
checksum = "sha256:60e7f43b4087710a2f9a72efd421741f5ca6267c6a027197a19a1bc060516e99"
url = "https://github.com/prefix-dev/pixi/releases/download/v0.68.0/pixi-x86_64-unknown-linux-musl.tar.gz"
url_api = "https://api.github.com/repos/prefix-dev/pixi/releases/assets/415124524"

[[tools.python]]
version = "3.14.10"
backend = "core:python"
specifiers = ["3.14.10"]

[tools.python."platforms.linux-arm64"]
checksum = "sha256:90f48337bc3dd4ae6eda81e001e4c981236d403b6b5a07778bbdf94ee423dc00"
url = "https://github.com/astral-sh/python-build-standalone/releases/download/20261026/cpython-3.14.10+20261026-aarch64-unknown-linux-gnu-install_only_stripped.tar.gz"
provenance = "github-attestations"

[tools.python."platforms.linux-x64"]
checksum = "sha256:4d3c16c59ed0802a3d1d34d8bce504b15ceeaff39d62dadecc6a9fa803412aed"
url = "https://github.com/astral-sh/python-build-standalone/releases/download/20261026/cpython-3.14.10+20261026-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz"
provenance = "github-attestations"

[[tools.uv]]
version = "0.13.6"
backend = "aqua:astral-sh/uv"
specifiers = ["0.13.6"]

[tools.uv."platforms.linux-arm64"]
checksum = "sha256:229e194255c7958d86d56e8790d969a6f74736592133f0dfd0076f754c642c1d"
url = "https://github.com/astral-sh/uv/releases/download/0.13.6/uv-aarch64-unknown-linux-gnu.tar.gz"
url_api = "https://api.github.com/repos/astral-sh/uv/releases/assets/679980412"
provenance = "github-attestations"

[tools.uv."platforms.linux-x64"]
checksum = "sha256:f29c3e94ffabd7e6f3ba521dba169a9b7ec12100bf3257087f0f24f6123f3645"
url = "https://github.com/astral-sh/uv/releases/download/0.13.6/uv-x86_64-unknown-linux-gnu.tar.gz"
url_api = "https://api.github.com/repos/astral-sh/uv/releases/assets/679980644"
provenance = "github-attestations"
LOCK

# Optional github_token secret raises the GitHub API rate limit for mise's
# attestation checks; anonymous builds 403 on busy IPs.
# Secret mounts never persist into image layers, unlike build args.
RUN --mount=type=secret,id=github_token set -e \
 && if [ -s /run/secrets/github_token ]; then \
      export GITHUB_TOKEN="$(cat /run/secrets/github_token)"; \
    fi \
 && mkdir -p "$MISE_CACHE_DIR" "$MISE_DATA_DIR" \
 && MISE_LOCKED=1 mise install \
 && mise reshim \
 && rm -rf "$MISE_CACHE_DIR"/* /tmp/* \
 && rm -rf "$MISE_DATA_DIR"/installs/go/*/test \
           "$MISE_DATA_DIR"/installs/go/*/src/cmd \
 && find "$MISE_DATA_DIR"/installs/node/*/lib/node_modules/npm \
        \( -name man -o -name docs -o -name changelogs \) \
        -type d -prune -exec rm -rf {} + \
 && node --version \
 && go version \
 && python --version \
 && uv --version \
 && pixi --version \
 && gh --version \
 && kubectl version --client=true \
 && k9s version

# Google Workspace CLI (gws): the statically linked musl build from the GitHub
# release (the glibc build needs GLIBC_2.39, newer than bookworm). Pins match
# the release's per-file .sha256 assets and GitHub's asset digests; the
# release is marked immutable. Bump: scripts/pins.sh gws <version>
ARG GWS_VERSION=0.22.5
ARG GWS_PKG_SHA256_AMD64=4db473dde4b1ab872e4ff35d769b0d4af1f1a6441a605e79d5cf8ada9c87e920
ARG GWS_PKG_SHA256_ARM64=e700fe63524932b10ec2130b47ece90aa850e66005fe52ccfc4cf8767bf9919a
RUN set -e \
 && ARCH="${TARGETARCH:-$(dpkg --print-architecture)}" \
 && case "$ARCH" in \
      amd64) A=x86_64;  SUM=$GWS_PKG_SHA256_AMD64 ;; \
      arm64) A=aarch64; SUM=$GWS_PKG_SHA256_ARM64 ;; \
      *) echo "unsupported arch: $ARCH"; exit 1 ;; \
    esac \
 && aisb-fetch "https://github.com/googleworkspace/cli/releases/download/v${GWS_VERSION}/google-workspace-cli-${A}-unknown-linux-musl.tar.gz" /tmp/gws.tgz "$SUM" \
 && mkdir /tmp/gws \
 && tar -xzf /tmp/gws.tgz -C /tmp/gws --no-same-owner \
 && install -o root -g root -m 0755 /tmp/gws/gws /usr/local/bin/gws \
 && rm -rf /tmp/gws /tmp/gws.tgz \
 && gws --version

# ---------- agents ----------
# claude, codex and pi live under /opt/aisb, owned by root and not writable by
# the agent user, with /usr/local/bin symlinks. A process running as the agent
# user (an npm postinstall script, a self-updater) cannot replace them without
# sudo. The agent user does have passwordless sudo (see below), so this is not
# a hard boundary: `aisb verify` reads the files out of a running sandbox
# through the Docker daemon and compares them with the *_BIN_SHA256_* pins.
# Each tool keeps two kinds of pins:
#   *_PKG_SHA256_*  the downloaded release file, checked before unpacking
#   *_BIN_SHA256_*  the executable itself, checked after unpacking
# Bump any of them with scripts/pins.sh <tool> <version>; it re-verifies the
# publisher's signature or checksum file and prints the new ARG lines.

# Claude Code: the native binary for this exact version from Anthropic's
# release bucket (the file install.sh downloads). Pins are the per-platform
# checksums in that version's manifest.json, whose detached GPG signature
# (manifest.json.sig) was verified against Anthropic's release key
# 31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE
# (https://code.claude.com/docs/en/setup#binary-integrity-and-code-signing).
# The download is the executable, so one pin per arch covers both checks.
ARG CLAUDE_VERSION=2.1.311
ARG CLAUDE_BIN_SHA256_AMD64=86dc1fcc69498159eaf15f446613e847010f9036bfbbdffd4c49350be8489ada
ARG CLAUDE_BIN_SHA256_ARM64=2e2496c9a8065df93acb0fca2d341f59a6b8777649b2e025ab8c9bcd5eef61f3
RUN set -e \
 && ARCH="${TARGETARCH:-$(dpkg --print-architecture)}" \
 && case "$ARCH" in \
      amd64) A=x64;   SUM=$CLAUDE_BIN_SHA256_AMD64 ;; \
      arm64) A=arm64; SUM=$CLAUDE_BIN_SHA256_ARM64 ;; \
      *) echo "unsupported arch: $ARCH"; exit 1 ;; \
    esac \
 && install -d -o root -g root -m 0755 /opt/aisb /opt/aisb/claude \
 && aisb-fetch "https://downloads.claude.ai/claude-code-releases/${CLAUDE_VERSION}/linux-${A}/claude" /opt/aisb/claude/claude "$SUM" \
 && chown root:root /opt/aisb/claude/claude \
 && chmod 0755 /opt/aisb/claude/claude \
 && ln -sf /opt/aisb/claude/claude /usr/local/bin/claude \
 && claude --version

# Claude Code updates itself by default. Inside a sandbox the binary must stay
# the pinned one, so turn off every update path: DISABLE_AUTOUPDATER stops the
# background check and DISABLE_UPDATES also blocks `claude update` and
# `claude install` (https://code.claude.com/docs/en/setup#disable-auto-updates).
# Set both in the environment and as a managed settings drop-in, which user and
# project settings cannot override.
ENV DISABLE_AUTOUPDATER=1 DISABLE_UPDATES=1
# (Written by RUN, not COPY --chmod: that mode would also land on the parent
# directories it creates, leaving them unreadable to Claude Code.)
RUN install -d -o root -g root -m 0755 /etc/claude-code/managed-settings.d \
 && cat > /etc/claude-code/managed-settings.d/50-aisb.json <<'JSON' \
 && chmod 0644 /etc/claude-code/managed-settings.d/50-aisb.json
{
  "env": {
    "DISABLE_AUTOUPDATER": "1",
    "DISABLE_UPDATES": "1"
  }
}
JSON

# Codex: the codex-package tarball from the GitHub release (the same files the
# npm package @openai/codex-linux-* ships, without the Node launcher). Package
# pins match the release's codex-package_SHA256SUMS and GitHub's asset
# digests. The bin/codex pins were checked against OpenAI's Sigstore signature
# (codex-<triple>.sigstore): the signature verifies, the signing certificate
# chains to Sigstore's Fulcio root and names
# github.com/openai/codex/.github/workflows/rust-release.yml at the release
# tag, and the Rekor log entry records the same SHA-256.
ARG CODEX_VERSION=0.164.0
ARG CODEX_PKG_SHA256_AMD64=3ec55053109608d5038f7e50750f7d25f768dfd740fef071aa710e2559b63aac
ARG CODEX_PKG_SHA256_ARM64=5918add6c7e9cf1e14a9af1b4c0f4bf1d24a43903faac26df5dc4c019ea91bd5
ARG CODEX_BIN_SHA256_AMD64=b93f6811d3f653f0fb83be4050d857fb9abbb111dfa2ce160563327daff7f306
ARG CODEX_BIN_SHA256_ARM64=8813c8d8a4cea2eeba271b72ab22f49628b184b24ab18dbef06b4ce41e9a8d39
RUN set -e \
 && ARCH="${TARGETARCH:-$(dpkg --print-architecture)}" \
 && case "$ARCH" in \
      amd64) T=x86_64-unknown-linux-musl;  SUM=$CODEX_PKG_SHA256_AMD64; BIN=$CODEX_BIN_SHA256_AMD64 ;; \
      arm64) T=aarch64-unknown-linux-musl; SUM=$CODEX_PKG_SHA256_ARM64; BIN=$CODEX_BIN_SHA256_ARM64 ;; \
      *) echo "unsupported arch: $ARCH"; exit 1 ;; \
    esac \
 && aisb-fetch "https://github.com/openai/codex/releases/download/rust-v${CODEX_VERSION}/codex-package-${T}.tar.gz" /tmp/codex.tgz "$SUM" \
 && install -d -o root -g root -m 0755 /opt/aisb/codex \
 && tar -xzf /tmp/codex.tgz -C /opt/aisb/codex --no-same-owner \
 && aisb-fetch - /opt/aisb/codex/bin/codex "$BIN" \
 && ln -sf /opt/aisb/codex/bin/codex /usr/local/bin/codex \
 && rm /tmp/codex.tgz \
 && codex --version

# pi: the release tarball from GitHub. Package pins match the release's
# SHA256SUMS and GitHub's asset digests (pi publishes no signature).
ARG PI_VERSION=1.4.0
ARG PI_PKG_SHA256_AMD64=f2e4e4329603223d03260cdb8bd5033dbdc1f73ba58ab8939db2e56546e115d2
ARG PI_PKG_SHA256_ARM64=fa5829c7ef83ffeaebba015715c8306220f89b2aa8a00154efbba98dd5440156
ARG PI_BIN_SHA256_AMD64=0d7611d0b86d38ec8f65dda8e8bee9c5e907c7bae381e9cfff9161b21e8087d0
ARG PI_BIN_SHA256_ARM64=26507c6bbf0beef1c5ef109dea333ddb73c058f9f75fed1de13f4186d73e919e
RUN set -e \
 && ARCH="${TARGETARCH:-$(dpkg --print-architecture)}" \
 && case "$ARCH" in \
      amd64) A=x64;   SUM=$PI_PKG_SHA256_AMD64; BIN=$PI_BIN_SHA256_AMD64 ;; \
      arm64) A=arm64; SUM=$PI_PKG_SHA256_ARM64; BIN=$PI_BIN_SHA256_ARM64 ;; \
      *) echo "unsupported arch: $ARCH"; exit 1 ;; \
    esac \
 && aisb-fetch "https://github.com/earendil-works/pi/releases/download/v${PI_VERSION}/pi-linux-${A}.tar.gz" /tmp/pi.tgz "$SUM" \
 && tar -xzf /tmp/pi.tgz -C /opt/aisb --no-same-owner \
 && aisb-fetch - /opt/aisb/pi/pi "$BIN" \
 && ln -sf /opt/aisb/pi/pi /usr/local/bin/pi \
 && rm /tmp/pi.tgz \
 && pi --version

# Nothing under /opt/aisb may be writable by anyone but root.
RUN chown -R root:root /opt/aisb \
 && chmod -R go-w /opt/aisb

# Create non-root agent user with HOME matching host's so bind-mounted ~/.pi/agent
# resolves at the same path inside the container. -o allows duplicate uid/gid
# (gid 20 conflicts with debian's `dialout`).
#
# The group is named after the user ("agent"), like a Linux user-private group.
# Hosts with umask 002 (Ubuntu's default) create group-writable dirs, and zsh's
# compaudit only accepts those when the group is the user's own private group,
# matched by name. Any other name makes zsh/oh-my-zsh in a mounted home prompt
# "insecure directories" on every shell.
#
# Pre-create the agent state dirs (~/.pi/agent, ~/.claude, ~/.codex, ~/dev,
# ~/sb-shared) and chown to agent. Without this, docker auto-creates them as root
# when individual files inside are bind-mounted, leaving the parent dir
# un-writable for agent (pi tries to mkdir bin/, write settings.json.lock, etc. —
# all fail with EACCES).
RUN set -e \
 && groupadd -o -g "$AGENT_GID" agent \
 && useradd  -o -u "$AGENT_UID" -g "$AGENT_GID" -d "$AGENT_HOME" -M -s /bin/zsh agent \
 && mkdir -p \
      "$AGENT_HOME/.pi/agent" \
      "$AGENT_HOME/.pi/agent/bin" \
      "$AGENT_HOME/.claude" \
      "$AGENT_HOME/.codex" \
      "$AGENT_HOME/dev" \
      "$AGENT_HOME/sb-shared" \
 && chown -R agent:agent "$AGENT_HOME" \
 && chown -R agent:agent "$MISE_DATA_DIR" "$MISE_CONFIG_DIR" "$MISE_CACHE_DIR" \
 && echo 'agent ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/agent \
 && chmod 0440 /etc/sudoers.d/agent

# ~/.local/bin stays on PATH for tools the user installs (uv tool, pipx), but
# last, so nothing dropped there shadows the pinned agents.
ENV PATH=${PATH}:${AGENT_HOME}/.local/bin

# Confirm shims resolve under the unprivileged user too — catches perm bugs at
# build time rather than first `aisb` shell.
RUN set -e \
 && su -s /bin/sh agent -c ' \
      mise    --version && \
      node    --version && \
      go      version   && \
      python  --version && \
      uv      --version && \
      pixi    --version && \
      gh      --version && \
      gws     --version && \
      kubectl version --client=true && \
      k9s     version && \
      claude  --version && \
      codex   --version && \
      pi      --version    \
    '

# The download helper is build-time only.
RUN rm -f /usr/local/sbin/aisb-fetch

USER agent
WORKDIR ${AGENT_HOME}

# oh-my-zsh — clone at pinned commit, write minimal zshrc with agent prompt.
RUN set -e \
 && git clone https://github.com/ohmyzsh/ohmyzsh.git "$HOME/.oh-my-zsh" \
 && git -C "$HOME/.oh-my-zsh" -c advice.detachedHead=false checkout "$OMZ_SHA" \
 && rm -rf "$HOME/.oh-my-zsh/.git" \
 && find "$HOME/.oh-my-zsh/plugins" -mindepth 1 -maxdepth 1 -type d \
        ! -name git -exec rm -rf {} + \
 && find "$HOME/.oh-my-zsh/themes" -mindepth 1 -maxdepth 1 -type f \
        ! -name 'robbyrussell.zsh-theme' -exec rm -f {} + \
 && cat > "$HOME/.zshrc" <<'ZSHRC'
export ZSH="$HOME/.oh-my-zsh"
ZSH_THEME="robbyrussell"
plugins=(git)
source $ZSH/oh-my-zsh.sh
PROMPT='%F{cyan}  agent%f %F{magenta}%m%f %F{blue}%~%f %F{green}❯%f '
ZSHRC

# tini = small init that reaps zombies + handles signals cleanly. Keeps container
# alive for `docker exec` sessions without needing `sleep infinity`.
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["sleep", "infinity"]
