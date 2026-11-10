#!/usr/bin/env bash
# pins.sh: fetch and verify the publisher's checksums for a new version of one
# pinned artifact, and print the Dockerfile lines to paste.
#
#   scripts/pins.sh claude <version>        e.g. 2.1.311
#   scripts/pins.sh codex  <version>        e.g. 0.164.0
#   scripts/pins.sh pi     <version>        e.g. 1.4.0
#   scripts/pins.sh mise   <version>        e.g. 2026.10.17
#   scripts/pins.sh gws    <version>        e.g. 0.22.5
#   scripts/pins.sh image  <name:tag>       e.g. debian:bookworm-20261104-slim
#   scripts/pins.sh mise-lock <image>       e.g. ai-sandbox-pi:latest
#
# mise-lock regenerates the Dockerfile's mise.lock for the [tools] in its
# config.toml, using the pinned mise inside an already built image (run it
# from the repository root). Paste the output between COPY <<'LOCK' and LOCK.
#
# Each check fails loudly (non-zero exit, nothing printed to paste) when a
# signature, checksum file or GitHub asset digest disagrees. It also prints
# the OSV advisories for the version. It installs nothing: it needs curl, jq,
# tar, gpg (claude, mise) and openssl (codex), and works in a temp dir.
#
# Set GITHUB_TOKEN to raise the GitHub API rate limit.
set -euo pipefail

die() { echo "pins.sh: $*" >&2; exit 1; }
say() { echo "pins.sh: $*" >&2; }

[ $# -eq 2 ] || { sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
tool=$1 ver=$2
case "$ver" in *[!A-Za-z0-9._:/-]*|'') die "bad version: $ver" ;; esac

if [ "$tool" = mise-lock ]; then
  # Runs before the temp-dir setup: it reads ./Dockerfile.
  [ -f Dockerfile ] || die "run mise-lock from the repository root"
  config=$(sed -n "/^COPY <<'TOML' \/etc\/mise\/config.toml\$/,/^TOML\$/p" Dockerfile | sed '1d;$d')
  [ -n "$config" ] || die "no mise config.toml block in Dockerfile"
  printf '%s\n' "$config" | docker run --rm -i --entrypoint sh -e MISE_CONFIG_DIR=/tmp/m \
    -e MISE_DATA_DIR=/tmp/d -e MISE_CACHE_DIR=/tmp/c "$ver" -c \
    'mkdir -p /tmp/m && cat > /tmp/m/config.toml && touch /tmp/m/mise.lock && cd / &&
     mise lock -g --platform linux-arm64,linux-x64 >&2 && cat /tmp/m/mise.lock'
  exit
fi

for c in curl jq tar; do command -v "$c" >/dev/null || die "$c not found"; done
if command -v sha256sum >/dev/null; then sha() { sha256sum "$1" | cut -d' ' -f1; }
else sha() { shasum -a 256 "$1" | cut -d' ' -f1; }; fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"

get() { curl -fsSL --proto '=https' --retry 3 -o "$2" "$1"; }

gh_api() {
  local auth=()
  [ -n "${GITHUB_TOKEN:-}" ] && auth=(-H "Authorization: Bearer $GITHUB_TOKEN")
  curl -fsSL --proto '=https' "${auth[@]}" "https://api.github.com/$1"
}

# asset_digest REPO TAG NAME: GitHub's server-side SHA-256 of a release asset.
asset_digest() {
  gh_api "repos/$1/releases/tags/$2" \
    | jq -er --arg n "$3" '.assets[] | select(.name == $n) | .digest | sub("^sha256:"; "")' \
    || die "no GitHub digest for $3 in $1 $2"
}

# same LABEL A B: fail unless two independently obtained checksums agree.
same() { [ "$2" = "$3" ] || die "$1: $2 != $3"; }

# gpg_verify FPR SIG [DATA]: good signature by exactly this key fingerprint.
gpg_verify() {
  local fpr=$1; shift
  gpg --batch --status-fd 1 --verify "$@" 2>/dev/null | grep -q "^\[GNUPG:\] VALIDSIG $fpr " \
    || die "no valid signature by $fpr on $1"
}

osv() { # osv ECOSYSTEM NAME VERSION
  local ids
  ids=$(curl -fsS https://api.osv.dev/v1/query -H 'content-type: application/json' \
    -d "{\"package\":{\"name\":\"$2\",\"ecosystem\":\"$1\"},\"version\":\"$3\"}" \
    | jq -r '[.vulns // [] | .[].id] | join(" ")')
  if [ -n "$ids" ]; then say "OSV advisories for $2 $3: $ids (review before pinning)"
  else say "OSV: no advisories for $2 $3"; fi
}

case "$tool" in
claude)
  # https://code.claude.com/docs/en/setup#binary-integrity-and-code-signing
  command -v gpg >/dev/null || die "gpg not found"
  export GNUPGHOME="$work/gnupg"; mkdir -m 700 "$GNUPGHOME"
  fpr=31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE
  base=https://downloads.claude.ai/claude-code-releases/$ver
  get https://downloads.claude.ai/keys/claude-code.asc key.asc
  gpg --batch --quiet --import key.asc
  gpg --batch --with-colons --fingerprint | grep -q "^fpr:::::::::$fpr:" || die "key is not $fpr"
  get "$base/manifest.json" manifest.json
  get "$base/manifest.json.sig" manifest.json.sig
  gpg_verify "$fpr" manifest.json.sig manifest.json
  same "manifest version" "$(jq -r .version manifest.json)" "$ver"
  amd=$(jq -er '.platforms["linux-x64"].checksum' manifest.json)
  arm=$(jq -er '.platforms["linux-arm64"].checksum' manifest.json)
  say "manifest.json for $ver: good signature by $fpr"
  osv npm @anthropic-ai/claude-code "$ver"
  printf 'ARG CLAUDE_VERSION=%s\nARG CLAUDE_BIN_SHA256_AMD64=%s\nARG CLAUDE_BIN_SHA256_ARM64=%s\n' "$ver" "$amd" "$arm"
  ;;

codex)
  command -v openssl >/dev/null || die "openssl not found"
  repo=openai/codex tag=rust-v$ver
  base=https://github.com/$repo/releases/download/$tag
  get "$base/codex-package_SHA256SUMS" sums
  # Sigstore's trusted root, for the Fulcio CA that issues release signing certs.
  get https://raw.githubusercontent.com/sigstore/root-signing/main/targets/trusted_root.json trusted_root.json
  jq -r '.certificateAuthorities[] | select(.validFor.end == null) | .certChain.certificates[]
         | "-----BEGIN CERTIFICATE-----\n" + (.rawBytes | gsub("(?<x>.{64})"; "\(.x)\n")) + "\n-----END CERTIFICATE-----"' \
    trusted_root.json | sed '/^$/d' > fulcio.crt
  identity="URI:https://github.com/$repo/.github/workflows/rust-release.yml@refs/tags/$tag"
  out=$(printf 'ARG CODEX_VERSION=%s\n' "$ver")
  for pair in AMD64:x86_64 ARM64:aarch64; do
    A=${pair%%:*} t=${pair#*:}-unknown-linux-musl
    pkg=codex-package-$t.tar.gz
    get "$base/$pkg" "$pkg"
    s=$(sha "$pkg")
    same "$pkg vs codex-package_SHA256SUMS" "$s" "$(awk -v n="$pkg" '$2 == n {print $1}' sums)"
    same "$pkg vs GitHub digest" "$s" "$(asset_digest "$repo" "$tag" "$pkg")"
    mkdir "x-$t"; tar -xzf "$pkg" -C "x-$t" --no-same-owner
    b=$(sha "x-$t/bin/codex")
    # The release signs the bare binary; check that signature covers this file.
    get "$base/codex-$t.sigstore" sig.json
    jq -r .cert sig.json | openssl base64 -d -A > leaf.crt
    jq -r .base64Signature sig.json | openssl base64 -d -A > sig.der
    openssl x509 -in leaf.crt -pubkey -noout > leaf.pub
    openssl dgst -sha256 -verify leaf.pub -signature sig.der "x-$t/bin/codex" >/dev/null \
      || die "Sigstore signature does not cover bin/codex in $pkg"
    openssl x509 -in leaf.crt -noout -ext subjectAltName | grep -qxF "    $identity" \
      || die "signing cert for $t is not $identity"
    when=$(jq -r .rekorBundle.Payload.integratedTime sig.json)
    openssl verify -attime "$when" -partial_chain -CAfile fulcio.crt leaf.crt >/dev/null \
      || die "signing cert for $t does not chain to Sigstore's Fulcio CA"
    idx=$(jq -r .rekorBundle.Payload.logIndex sig.json)
    logged=$(curl -fsS "https://rekor.sigstore.dev/api/v1/log/entries?logIndex=$idx" \
      | jq -r '.[].body' | openssl base64 -d -A | jq -r .spec.data.hash.value)
    same "bin/codex vs Rekor entry $idx" "$b" "$logged"
    say "$t: package matches SHA256SUMS and GitHub; bin/codex signed by $identity, logged in Rekor"
    out+=$(printf '\nARG CODEX_PKG_SHA256_%s=%s\nARG CODEX_BIN_SHA256_%s=%s' "$A" "$s" "$A" "$b")
  done
  osv npm @openai/codex "$ver"
  echo "$out"
  ;;

pi)
  repo=earendil-works/pi tag=v$ver
  base=https://github.com/$repo/releases/download/$tag
  get "$base/SHA256SUMS" sums
  out=$(printf 'ARG PI_VERSION=%s\n' "$ver")
  for pair in AMD64:x64 ARM64:arm64; do
    A=${pair%%:*} pkg=pi-linux-${pair#*:}.tar.gz
    get "$base/$pkg" "$pkg"
    s=$(sha "$pkg")
    same "$pkg vs SHA256SUMS" "$s" "$(awk -v n="$pkg" '$2 == n {print $1}' sums)"
    same "$pkg vs GitHub digest" "$s" "$(asset_digest "$repo" "$tag" "$pkg")"
    mkdir "x-$A"; tar -xzf "$pkg" -C "x-$A" --no-same-owner
    out+=$(printf '\nARG PI_PKG_SHA256_%s=%s\nARG PI_BIN_SHA256_%s=%s' "$A" "$s" "$A" "$(sha "x-$A/pi/pi")")
  done
  say "pi $ver: tarballs match SHA256SUMS and GitHub digests (pi publishes no signature)"
  osv npm @earendil-works/pi-coding-agent "$ver"
  echo "$out"
  ;;

mise)
  # https://mise.jdx.dev/installing-mise.html
  command -v gpg >/dev/null || die "gpg not found"
  export GNUPGHOME="$work/gnupg"; mkdir -m 700 "$GNUPGHOME"
  fpr=24853EC9F655CE80B48E6C3A8B81C9D17413A06D
  base=https://github.com/jdx/mise/releases/download/v$ver
  get "https://keys.openpgp.org/vks/v1/by-fingerprint/$fpr" key.asc
  gpg --batch --quiet --import key.asc
  get "$base/SHASUMS256.asc" SHASUMS256.asc
  gpg_verify "$fpr" SHASUMS256.asc
  gpg --batch --quiet --decrypt SHASUMS256.asc 2>/dev/null > signed.txt
  pick() { awk -v n="./mise-v$ver-linux-$1.tar.gz" '$2 == n {print $1}' signed.txt | grep . || die "no $1 entry"; }
  amd=$(pick x64) arm=$(pick arm64)
  same "linux-x64 vs GitHub digest" "$amd" "$(asset_digest jdx/mise "v$ver" "mise-v$ver-linux-x64.tar.gz")"
  same "linux-arm64 vs GitHub digest" "$arm" "$(asset_digest jdx/mise "v$ver" "mise-v$ver-linux-arm64.tar.gz")"
  say "SHASUMS256 for $ver: good signature by $fpr"
  osv crates.io mise "$ver"
  printf 'ARG MISE_VERSION=%s\nARG MISE_PKG_SHA256_AMD64=%s\nARG MISE_PKG_SHA256_ARM64=%s\n' "$ver" "$amd" "$arm"
  ;;

gws)
  repo=googleworkspace/cli tag=v$ver
  base=https://github.com/$repo/releases/download/$tag
  out=$(printf 'ARG GWS_VERSION=%s\n' "$ver")
  for pair in AMD64:x86_64 ARM64:aarch64; do
    A=${pair%%:*} pkg=google-workspace-cli-${pair#*:}-unknown-linux-musl.tar.gz
    get "$base/$pkg.sha256" "$pkg.sha256"
    s=$(cut -d' ' -f1 "$pkg.sha256")
    same "$pkg.sha256 vs GitHub digest" "$s" "$(asset_digest "$repo" "$tag" "$pkg")"
    out+=$(printf '\nARG GWS_PKG_SHA256_%s=%s' "$A" "$s")
  done
  say "gws $ver: .sha256 assets match GitHub digests"
  osv npm @googleworkspace/cli "$ver"
  echo "$out"
  ;;

image)
  # Resolve name:tag to the digest of its multi-arch index.
  ref=$ver name=${ref%:*} tag=${ref##*:}
  [ "$name" != "$ref" ] || die "image needs name:tag"
  accept='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json'
  case "$name" in
  gcr.io/*)
    path=${name#gcr.io/}
    digest=$(curl -fsSI -H "Accept: $accept" "https://gcr.io/v2/$path/manifests/$tag" \
      | tr -d '\r' | awk -F': ' 'tolower($1) == "docker-content-digest" {print $2}') ;;
  */*/*) die "only Docker Hub and gcr.io images are supported" ;;
  *)
    path=$name; case "$path" in */*) ;; *) path=library/$path ;; esac
    token=$(curl -fsS "https://auth.docker.io/token?service=registry.docker.io&scope=repository:$path:pull" | jq -r .token)
    digest=$(curl -fsSI -H "Authorization: Bearer $token" -H "Accept: $accept" \
      "https://registry-1.docker.io/v2/$path/manifests/$tag" \
      | tr -d '\r' | awk -F': ' 'tolower($1) == "docker-content-digest" {print $2}') ;;
  esac
  case "$digest" in sha256:????????????????????????????????????????????????????????????????) ;; *) die "no digest for $ref" ;; esac
  echo "FROM $ref@$digest"
  ;;

*) die "unknown tool: $tool (claude, codex, pi, mise, gws, image, mise-lock)" ;;
esac
