# Design: Copies — One-Way File Copy into Sandbox

## Summary

Introduce a `copies` config directive that copies files and directories from the
host into the sandbox at container creation time, using `docker cp -a`. Unlike
bind mounts (which are bidirectional), copies are strictly one-way: the sandbox
can read and write its private copy, but no data ever propagates back to the
host. The primary use case is safe cache warming — pre-populating Go module
caches, pip caches, npm caches, and similar download directories without
exposing the host's cache to potential poisoning by a compromised agent.

## Goals

- Provide a safe, one-way mechanism to pre-populate sandbox caches from the
  host, eliminating the cache-poisoning risk inherent in bind-mounting cache
  directories.
- Keep the config surface minimal and consistent with the existing mount syntax.
- Make copies async by default (no added startup latency) with an opt-in
  `--wait-copies` flag for when the user wants the container fully warmed before
  entering the shell.
- Integrate cleanly with `aisb up`. Copies only happen on initial container
  creation (copy-on-birth), not on subsequent starts of an existing container.
- Support the same template variable expansion (`{{HOME}}`, `{{CWD}}`,
  `{{SHARED_DIR}}`, `~/`, `$VAR`) and `src:dest` remapping as mounts.

## Non-Goals

- **Bidirectional sync or back-propagation.** Copies are strictly one-way.
  There is no mechanism to flush container changes back to the host.
- **Exclusion or filtering patterns.** V1 copies are all-or-nothing per entry.
  Exclude patterns (e.g. `__pycache__`, `*.o`, `.git`) are deferred.
- **Copies in `aisb create`.** The `create` subcommand (used by external tools
  for non-interactive container preparation) skips copies entirely. See
  Implementation — Handler Changes for the rationale.
- **Mount deprecation.** Mounts remain the mechanism for project files, config
  files, and any directory where bidirectional access is intended. Copies are
  an additional tool, not a replacement.
- **Refresh or reconciliation.** Once copied, the sandbox cache evolves
  independently. On container recreation (`aisb rm` + `aisb`), a fresh copy is
  taken from the host.

## Background / Motivation

`aisb` uses Docker bind mounts (`-v`) to give the sandbox access to the host
filesystem. This works well for project files and config files — the user wants
to edit on the host and have changes visible inside the container, and vice
versa.

Caches, however, are a different story. It is common to mount `~/.cache/pip`,
`~/go/pkg/mod`, or `~/.npm` into the sandbox so the agent does not re-download
packages from scratch. But a compromised agent inside the sandbox could write
malicious content into these cache directories, poisoning the host cache.
Because bind mounts are bidirectional, every file written inside the container
is immediately visible on the host. The next time the user or another sandbox
uses that cache, the malicious content is served.

The fix is a one-way copy: take a snapshot of the host cache at container
creation time, place it inside the container, and sever the connection. The
sandbox can do whatever it wants to its copy; the host is unaffected.

Docker does not support a "copy, don't mount" flag on `docker run`. The
natural mechanism is `docker cp` after the container is running.

## Design

### Config Format

Copies live alongside mounts in the JSON config file, using the identical
declarative syntax:

```json
{
  "default": {
    "mounts": [
      "{{HOME}}/.gitconfig",
      "{{HOME}}/.claude/settings.json",
      "{{CWD}}"
    ],
    "copies": [
      "{{HOME}}/.cache/pip",
      "{{HOME}}/go/pkg/mod:{{HOME}}/.cache/go-mod",
      "{{HOME}}/.npm"
    ]
  },
  "projects": {
    "/home/me/dev/ai-project": {
      "extra_mounts": ["~/dev/shared-lib"],
      "extra_copies": ["~/big-models"]
    }
  }
}
```

Each entry follows the same rules as mounts:

- `"src"` — copy the host path to the same path inside the container.
- `"src:dest"` — copy the host path to a different path inside the container.
- Placeholders: `{{HOME}}`, `{{CWD}}`, `{{SHARED_DIR}}`, `~/`, `$VAR` are all
  expanded identically to mount entries.
- Missing source paths are **skipped with a warning** (same as mounts).

The `Project` struct gains two new fields:

| Field         | Type       | JSON key        | Semantics                  |
|---------------|------------|-----------------|----------------------------|
| `Copies`      | `[]string` | `"copies"`      | Replaces default copies    |
| `ExtraCopies` | `[]string` | `"extra_copies"`| Appended after copies      |

And `Effective` gains a single `Copies []string` field (merged by `Resolve`).

### Placeholder Expansion & Resolution

Copy entries are resolved by the same `mountresolver` package (or a shared
resolver it exports). The expansion logic is identical; only the deduplication
differs (copies are deduped by source path, not by destination, since multiple
copies to the same destination are harmless but wasteful).

A small helper `mountresolver.ResolveCopies` or a shared `expandEntries` is
extracted so both mounts and copies reuse the same expansion/dedup code.

### Copy Lifecycle

Copies happen **only on initial container creation**, not when an existing
stopped container is started. The flow:

1. `Handler.ensure()` creates a new container via `dx.Create()` → container is
   running (created with `docker run -d`).
2. After creation succeeds, if `Effective.Copies` is non-empty, copies are
   performed.
3. On subsequent `aisb` invocations when the container already exists, copies
   are skipped — the container already has its cache.
4. On `aisb rm` + `aisb` (recreation), copies run again since it is a new
   birth.

**Why copy-on-birth only:** The goal is cache warming. Re-copying on every
start would overwrite any packages the agent downloaded, which is wasteful and
surprising. If the user wants a fresh cache, they `aisb rm` and start over.

### Async vs. Sync

| Mode          | Trigger              | Behavior                                                   |
|---------------|----------------------|------------------------------------------------------------|
| **Async**     | Default              | Copies are launched as detached `docker cp` subprocesses.  |
|               |                      | A message is logged ("warming caches in background...").   |
|               |                      | The shell opens immediately (no wait).                     |
|               |                      | Subprocesses are reparented to init and complete even if   |
|               |                      | the shell exits before they finish.                        |
| **Sync**      | `--wait-copies` flag | Copies run sequentially with per-entry logging.            |
|               |                      | The shell only opens after all copies complete.            |

Rationale: Most of the time the user does not need every byte of the cache on
startup. The agent downloads or accesses packages lazily, so the first fetch
may be slightly slower if the copy is still in progress — but that is far
better than blocking the shell for 10-30 seconds on every `aisb`.

The `--wait-copies` flag is useful when the user knows they will immediately
run a cache-heavy operation (e.g. `go build ./...`) and wants the full cache
present before any command runs.

### CLI Flag

The flag is added to the `aisb up` subcommand (which currently has none):

```
aisb up --wait-copies
```

Or equivalently:

```
aisb --wait-copies
```

Since `aisb` (no subcommand) defaults to `up`, the flag can be passed directly:

```
aisb --wait-copies
```

Flag name: `--wait-copies` (bool). Parsed in `main.go` before dispatching.

### Skipping for `aisb create`

The `create` subcommand is used non-interactively by external tools (e.g.
darb). It creates a configured container and prints its name — it does not
start a shell. Copies are **skipped** for `create` because:

1. `create` may be used in contexts where the caller wants a clean container
   without cache warming.
2. The external tool may have its own caching strategy.
3. Copies would require starting the container (create currently only calls
   `docker create`), changing the contract for existing consumers.

A code comment at the call site in `Handler.Create()` documents this:

```go
// Copies are intentionally skipped for create (non-interactive).
// External tools using create may have their own caching strategy,
// and copies would also force a container start (docker cp requires
// a running container), changing the non-interactive contract.
```

### Copy Mechanism

Each copy entry is executed via:

```
docker cp -a <expanded-source-path> <container-name>:<expanded-dest-path>
```

The `-a` (archive) flag copies with ownership and permissions preserved. Since
the host UID/GID matches the sandbox user (the Docker image is built with
`AGENT_UID`/`AGENT_GID`), permissions are correct without a post-copy
`chown`.

**Directory semantics:** When the source is a directory, `docker cp` creates
the destination directory in the container if it does not exist. When the
destination already exists, the source directory's contents are copied into it
(not overlaid). This matches the expected behaviour: if `~/.cache/pip` exists
on both sides, the host's files appear inside the container's `~/.cache/pip/`,
and any pre-existing container files with the same names are overwritten.

**Ordering:** Copies are processed sequentially (under sync mode) or launched
sequentially (under async mode). There is no parallelism, because cache
directories tend to be I/O-bound on the same disk and parallel copies mostly
contend for bandwidth.

### Error Handling

- **Async mode:** Errors are logged to stderr but do not block the shell or
  abort subsequent copies. A copy that fails halfway leaves a partial directory
  — this is acceptable because the cache is purely a performance optimization.
- **Sync mode:** On error, the failed copy is reported, subsequent copies
  continue, and the shell opens anyway. A copy failure never prevents the user
  from entering the sandbox.

## Implementation

### 1. Config layer (`internal/cfg/cfg.go`)

```go
type Project struct {
    // ... existing fields ...
    Copies      []string `json:"copies,omitempty"`
    ExtraCopies []string `json:"extra_copies,omitempty"`
}

type Effective struct {
    // ... existing fields ...
    Copies []string
}
```

In `Resolve`, after applying mounts/extra_mounts:

```go
cfg.Copies = append(cfg.Copies, p.Copies...)
// (in the apply closure)
// ExtraCopies similarly appended in the main loop after all project
// matching is done, analogous to ExtraMounts.
```

### 2. Resolver reuse (`internal/mountresolver/resolver.go`)

Extract the placeholder expansion logic so it can be shared. Either:

- Export `ExpandEntry(string, Env) (src, dest string)` and let both
  `Resolve` and the copy code call it.
- Add `ResolveCopies(copies, extraCopies []string, env Env, log Warner) []CopySpec`
  that returns structured `{Source, Dest}` pairs.

The latter is cleaner because copy specs don't need the `src:dest` string
format — they are consumed programmatically.

```go
type CopySpec struct {
    Source string // expanded host path
    Dest   string // expanded container path
}

func ResolveCopies(copies, extraCopies []string, env Env, log Warner) []CopySpec {
    // expand all entries, dedupe by source, filter missing sources
}
```

### 3. Docker layer (`internal/dx/dx.go`)

Add:

```go
func Copy(e Executor, src, dest string) error {
    // docker cp -a <src> <dest>
    return e.RunSilent("cp", "-a", src, dest)
}
```

Where `dest` is of the form `<container-name>:<path>`. The caller constructs
this.

### 4. Handler changes (`internal/cmd/cmd.go`)

**`Handler.ensure`:** After the container-creation branch succeeds, call
`h.doCopies` if there are copies to make (and the mode is not `create`).

**`Handler.doCopies`:** New private method that iterates `c.Copies` and
launches `docker cp -a` for each.

The signature:

```go
func (h Handler) doCopies(name string, copies []mountresolver.CopySpec, wait bool) error
```

- If `wait == true`: sequential, with `h.Log.Step("copying ...")` per entry.
- If `wait == false`: launch each as a detached `exec.Command`, log a single
  `"[aisb] warming caches in background..."`, and return immediately.

**`Handler.Up`:** Accept a `waitCopies bool` parameter (or the Handler carries
it). Calls `h.ensure(...)`, then `h.doCopies(...)`, then `dx.Shell(...)`.

**`Handler.Create`:** Ensure copies are skipped. Add the documented comment.

### 5. CLI parsing (`main.go`)

The `aisb up` or bare `aisb` invocation needs to parse `--wait-copies`. In
`main.go`, before the subcommand switch:

```go
waitCopies := false
if len(os.Args) == 2 && os.Args[1] == "--wait-copies" {
    waitCopies = true
    os.Args = os.Args[:1] // reset to bare "aisb" so the switch hits ""
} else if len(os.Args) > 1 && os.Args[1] == "up" {
    // check for --wait-copies after "up"
    // (simple approach: range os.Args for the flag)
}
```

For a clean implementation, use `flag.FlagSet` for the `up` subcommand
(similar to how `create` uses one), with a boolean flag.

### 6. Resolve copies in `ensure` / `Up`

In `Handler.ensure`, after `h.create(...)`:

```go
copies := mountresolver.ResolveCopies(c.Copies, c.ExtraCopies,
    mountresolver.Env{Home: home, CWD: cwd, SharedDir: c.SharedDir}, h.Log)
if len(copies) > 0 {
    h.doCopies(name, copies, waitCopies)
}
```

Note: `ensure` doesn't know about `waitCopies` currently. Pass it through as a
parameter or thread it via `Handler`. Simpler: make `waitCopies` a field on
`Handler` set during construction.

## Tradeoffs & Risks

### Startup Time (Async Mitigation)

**Risk:** Large cache copies (multi-GB) could introduce seconds-to-minutes of
latency.

**Mitigation:** Async default. The shell opens immediately; copies proceed in
the background. The user only pays the copy cost when they actually access
those cache files, and even then only the first access may be slower if the
copy is still in progress.

### Orphaned Background Processes

**Risk:** Async copies are detached child processes of `aisb`. If the user
exits the shell before copies finish, the children are reparented to PID 1 and
continue running. If the container is stopped or removed while copies are in
progress, `docker cp` will fail (harmlessly).

**Impact:** Low. A few orphaned `docker cp` processes that eventually fail and
exit. They consume minimal resources.

### Cache Divergence Surprise

**Risk:** A user who mounts `~/.cache/pip` (bidirectional) today and later
switches to copies may be surprised that packages their agent downloads are no
longer available on the host. The copies section in the config file and a
clear naming convention (`copies` vs `mounts`) should make the semantics
obvious.

**Mitigation:** Document the one-way semantics clearly in `README.md` and in
the config file help text. The names themselves (`copies` vs `mounts`) carry
the semantic weight.

### Symlinks in Cache Directories

**Risk:** `docker cp -a` preserves symlinks as-is (it's an archive copy). If a
symlink points outside the copied tree, the link is broken inside the
container. Some package managers (e.g. Go, pip) use symlinks internally.

**Impact:** Low. Most cache symlinks point within the same cache tree. If a
broken symlink is encountered, the package manager typically re-downloads or
recreates it. This is the same behaviour as `docker cp` for any use case.
Start with `docker cp -a` and only switch to a more sophisticated tar-pipe
approach if specific cache layouts prove problematic.

### Permission Mismatch

**Risk:** Files owned by a different UID on the host (e.g. root-owned files in
`/root/.cache`) could be copied with wrong ownership.

**Mitigation:** `docker cp -a` preserves ownership. Since the sandbox user has
the same UID as the host user, files owned by the host user are accessible.
Files owned by other users on the host (root, system users) are handled by
Docker's archive logic and become owned by the destination user inside the
container in most setups. If this proves problematic, a post-copy
`docker exec chown -R <uid> <dest>` can be added as an option.

## Testing Strategy

### Unit Tests

| Scope | What to test |
|-------|-------------|
| `cfg.Resolve` | Merging `copies` and `extra_copies` from config, same coverage as mounts. |
| `mountresolver.ResolveCopies` | Expansion, deduplication, missing-source filtering. |
| `dx.Copy` | Argument construction for `docker cp -a`. |

### Integration Tests

- **Happy path:** Create a container with copies defined, verify files are
  present inside (`docker exec`).
- **Missing source:** Define a copy with a non-existent source, verify it is
  skipped with a warning and the container still starts.
- **Async mode:** Verify that `aisb up` (without `--wait-copies`) returns to a
  shell immediately while copies are still in progress. (Hard to automate
  cleanly; manual test for v1.)
- **Sync mode:** Verify `aisb up --wait-copies` blocks until copies complete
  and all files are present.
- **Re-entry:** Start a container with copies, exit, re-enter. Verify copies
  are **not** re-executed (cache already present).
- **`aisb create`:** Verify copies are skipped and the container is created
  without copies.
- **Removal and recreation:** `aisb rm` then `aisb`. Verify copies run again
  and the new container has the cache.

### Manual Test Scenarios

1. Define a copy for a large cache directory (`~/.cache/pip`, ~200MB).
   Run `aisb` without `--wait-copies`. Confirm shell opens quickly. Check
   inside: files appear within a few seconds.
2. Same, with `--wait-copies`. Confirm shell blocks until copy done.

## Open Questions

*This section is intentionally empty. All open questions were resolved with
the user before committing this document.*
