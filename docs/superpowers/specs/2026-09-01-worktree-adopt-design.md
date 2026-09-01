# Worktree adopt / rm --keep-worktree — design

Date: 2026-09-01
Status: approved

## Problem

External worktree managers (Orca, an AI-agent worktree GUI) create worktrees
with their own built-in git operations and cannot be pointed at
`dx worktree create`. They do run a setup hook right after creating a worktree
(cwd = the new worktree, `/bin/bash`, 120s timeout) and an archive hook right
before deleting one.

Hand-writing `cp .env` / `dx db fork` / `mise run setup` in those hooks works
but bypasses `[[worktree.copy]]` and `[[worktree.init]]`, so a hook-created
worktree drifts from a `dx worktree create` one. `dx.toml` should stay the
single source of truth for what a prepared worktree looks like.

dx keeps no worktree registry — the DB name and service names derive from the
current git branch — so a worktree at an arbitrary path is already a
first-class citizen for `dx db url` and `dx worktree list`. Only the
create/remove halves of the lifecycle are missing a hook-shaped entry point.

## Design

### `dx worktree adopt [--skip-init]`

`createWorktree` is split so the two commands cannot drift:

```
create = collision check + git worktree add + adopt
adopt  = DB fork → copy → init
```

`adoptWorktree(adoptOpts{Branch, Path, SkipInit, Verb}, wtDeps)` holds the
three shared steps; `createWorktree` keeps only the git part and delegates.
`Verb` supplies the past-tense word in the success line ("created" /
"adopted"); the failure lines are phrased neutrally (`db fork failed (worktree
kept)`) so both callers can share them.

| Decision | Choice | Why |
|---|---|---|
| Where it runs | Inside the worktree only; no branch argument | Matches the hook's cwd. Running it from the primary is an error pointing at `create`. |
| Primary root | First entry of `git worktree list --porcelain` | `[[worktree.copy]]` sources are primary-relative, and an adopted worktree may live anywhere — the configured `worktree.dir` cannot be used to walk back. |
| Config source | `<worktree>/dx.toml` | Same as every other subcommand; the branch's own config governs its setup. |
| Slug collision | Checked against the *other* worktrees | The target is itself in `git worktree list`, so the existing `create` check would always self-trip. A real collision still aborts (two branches sharing one DB makes `rm` destructive). |
| Exit codes | 0 = ready, 3 = DB fork/copy/init failed, 1 = abort | Identical to `create`, so a hook can branch on the same codes. |

Idempotency needs no new code: `db.Container.Fork` and `db.SQLite.Seed`
already short-circuit on an existing target, and `runCopySteps` skips
destinations that exist. Init steps re-run by design — they are the project's
own commands and are expected to be re-runnable (`pnpm install`, `mise run
setup`).

### `dx worktree rm --keep-worktree`

Stops services and drops the DB; skips `git worktree remove` and the branch
delete. Orthogonal to `--keep-db` (both together stop services and nothing
else).

- **Dirty check skipped.** The checkout survives, so there is no uncommitted
  work to protect, and an archive hook would otherwise fail on any worktree
  with edits in it.
- **Rejected with `--delete-branch`** (rc 2): "keep the worktree" and "delete
  its branch" contradict each other.
- **The one `rm` form that also runs from inside the worktree.** `git worktree
  remove` cannot run against its own cwd, so the primary-checkout requirement
  is relaxed exactly where it is safe.

### Path resolution in `rm` (fallout fix)

`rmWorktree` computed its target as `<primary>/<worktree.dir>/<branch>`, which
is wrong for any worktree created outside dx — the dirty check, the registry
lookup and the service shutdown would all address the wrong directory. The
path now comes from `git worktree list --porcelain` (`rmOpts.Path`), falling
back to the computed layout when the branch is not listed.

## Testing

- `adopt`: never invokes git; no-DB → 0; url_env unset / container down / fork
  failure → 3; forks into the branch DB; an existing DB is not re-created;
  copy runs before init at the given path; copy and init failures → 3;
  `--skip-init` still copies; a second run stays 0 with init re-running.
- Collision: the target's own branch does not trip the check; another branch
  with the same slug aborts with 1.
- `rm --keep-worktree`: no `worktree remove`, no `branch -d`, DB still
  dropped, services still stopped, dirty needs no `--force`, drop failure
  still aborts.
- Path: a given path reaches the dirty and toplevel probes; an empty one falls
  back to the configured layout.
- Arg parsing: `--keep-worktree` with `--keep-db` is accepted, with
  `--delete-branch` is an error; `adopt` rejects a positional argument.
- E2E on a scratch repo: `git worktree add` to a path outside the primary
  tree, then `adopt` twice (copy + init observed, second run idempotent),
  `adopt` from the primary rejected, `rm` from inside rejected without the
  flag, `rm --keep-worktree` on a dirty worktree leaving both the checkout and
  the branch in place, and `rm` from the primary resolving the external path.

## Out of scope

- A worktree registry. Branch-derived naming already covers externally created
  worktrees.
- `dx worktree adopt <branch>` from the primary. The hook always runs inside
  the worktree; adding the form later would not change the exit contract.
- Teaching dx about Orca's env vars (`ORCA_WORKTREE_PATH` etc.). git is the
  source of truth, which keeps `adopt` usable from any tool, or by hand.
