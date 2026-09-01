package cli

import (
	"fmt"
	"io"
)

// version is stamped by release builds via
// -ldflags "-X github.com/agarichan/dx/internal/cli.version=<tag>".
var version = "dev"

const helpText = `dx — per-worktree dev environment CLI

USAGE:
  dx <command> [args]

COMMANDS:
  up                          start every [service.<key>] from dx.toml (background, idempotent)
  down                        stop this checkout's dev services
  serve <key>                 start one service from dx.toml
  status [--all] [--json]     service status (--all = across all checkouts)
  stop <name|key>             stop a service (global)
  logs [name|key] [-f] [-t] [--no-color]
                              tail logs, colored per-service prefix; -f to follow
  exec <key> [--] <cmd...>    run a command with <key>'s env (URLs + db_env) and dir
  db <sub>                    DB: up|down|psql|fork|drop|reset|list|url [--scheme <s>]
  worktree <sub>              worktree: create|adopt|rm|list (alias: wt)
  raycast <sub>               Raycast extension: install|uninstall
  update [--force]            self-update to the latest GitHub release
  version                     print version
  -h, --help                  show this help

LOGS:
  dx logs                     all services; colored per-service prefix
  dx logs <name|key>          one service only
  dx logs -f                  follow (tail -F)
  dx logs -t, --time          show capture timestamp (e.g. "myapp-api 12:07:23.567 | ...")
  dx logs --no-color          plain, no ANSI (auto when piped/captured)

WORKTREE:
  dx worktree create <branch> [--from <base>] [--skip-init]
  dx worktree adopt [--skip-init]        (inside a worktree created by another tool)
  dx worktree rm <branch> [--force] [--keep-db] [--keep-worktree] [--delete-branch]
  dx worktree list [--json]

DB (config from dx.toml [db]):
  dx db up|down|psql|fork|drop|reset|list|url

Config: dx.toml at the repo root.
`

const logsHelp = `dx logs — tail service logs (Go multiplexer, per-service colored prefix)

USAGE:
  dx logs [name|key] [-f] [-t] [--no-color]

ARGS:
  name|key            only this service (default: all services, merged by arrival order)

FLAGS:
  -f, --follow        follow (like tail -F)
  -t, --time          show the capture timestamp: "myapp-api 12:07:23.567 | ..."
                      (hidden by default; the time lives in the color log)
  --no-color, --plain plain output, no ANSI (also automatic when piped/captured)

NOTES:
  In a terminal each line is prefixed with the service name in a per-service color.
  The plain .log (what AI/grep read directly) is timestamp-free; -t reads the color log.
`

const serveHelp = `dx serve — start one service from dx.toml

USAGE:
  dx serve <key>

ARGS:
  key                 service key from dx.toml [service.<key>]

NOTES:
  Starts the service in the background via the log pump. Idempotent per service name.
`

const upHelp = `dx up — start all services from dx.toml

USAGE:
  dx up

NOTES:
  Starts every [service.<key>] declared in dx.toml. Idempotent: already-running
  services are skipped. Registers each service in the per-checkout state directory.
`

const downHelp = `dx down — stop this checkout's dev services

USAGE:
  dx down

NOTES:
  Stops all services registered for the current checkout and removes them from
  the registry. Services in other checkouts are unaffected.
`

const statusHelp = `dx status — show service status

USAGE:
  dx status [--all] [--json]

FLAGS:
  --all, -a    show services across all checkouts (not just current)
  --json       output as JSON array

FIELDS (JSON):
  name, root, state (running|stopped), pid, url, log
`

const stopHelp = `dx stop — stop a service by name or config key

USAGE:
  dx stop <name|key>

ARGS:
  name|key     portless service name or dx.toml service key

NOTES:
  Searches across all registered checkouts. Sends SIGTERM to the process group.
`

const dbHelp = `dx db — manage the project database

USAGE:
  dx db <sub>

SUBCOMMANDS:
  up       start the DB container defined in dx.toml [db]
  down     stop the DB container
  psql     open psql session
  fork     create a branch DB copy from the current state
  drop     drop the branch DB
  reset    drop + re-create (empty schema)
  list     list known DB instances
  url      print the connection URL for this checkout
             --scheme <s>   override the DSN scheme (e.g. postgresql+psycopg,
                            sqlite+aiosqlite)

Config: [db] section in dx.toml at repo root.
`

const execHelp = `dx exec — run a command in a service's environment

USAGE:
  dx exec <key> [--] <command...>

ARGS:
  key         service key from dx.toml [service.<key>]
  command     argv to run (foreground; exit code is propagated)

NOTES:
  The command gets the same env dx serve would inject (pub/internal URLs,
  db_env DSN, FORCE_COLOR) and runs in the service's dir. On a linked
  worktree the idempotent DB fork/seed runs first — so e.g.
  "dx exec api -- alembic upgrade head" works right after worktree create.
`

const worktreeHelp = `dx worktree — manage git worktrees

USAGE:
  dx worktree <sub>    (alias: dx wt <sub>)

SUBCOMMANDS:
  create <branch> [--from <base>] [--skip-init]
      Create a new worktree + linked DB fork, seed [[worktree.copy]] files, then
      run [[worktree.init]] steps. Runs from the primary checkout only.
      --from <base>  branch start point (new branch only)
      --skip-init    skip the configured init steps (copy still runs)
      Exit: 0 = ready, 3 = worktree kept but DB fork or init failed, 1 = abort.

  adopt [--skip-init]
      Everything 'create' does except 'git worktree add': DB fork, [[worktree.copy]]
      files, then [[worktree.init]] steps — against the worktree you are standing in.
      For worktrees created by another tool (e.g. an Orca setup hook), so that
      dx.toml stays the single source of truth. Runs from inside the worktree only;
      takes no branch argument (the current branch is the target).
      Idempotent — safe to re-run: an existing DB is not re-forked, existing copy
      destinations are kept, init steps re-run.
      --skip-init    skip the configured init steps (copy still runs)
      Exit: same as create — 0 = ready, 3 = DB fork/copy/init failed, 1 = abort.

  rm <branch> [--force] [--keep-db] [--keep-worktree] [--delete-branch]
      Remove worktree, its DB fork (unless --keep-db), and its services.
      --force          proceed even if the worktree has uncommitted changes
      --keep-db        skip DB drop
      --keep-worktree  stop services and drop the DB only; leave the checkout and
                       the branch in place (for a tool that removes them itself,
                       e.g. an Orca archive hook). Skips the dirty check, and is
                       the one form of 'rm' that also runs from inside the worktree.
                       Cannot be combined with --delete-branch.
      --delete-branch  also delete the git branch after removal

  list [--json]
      List worktrees with branch, path, DB name, and per-service state.

CONFIG ([worktree] in dx.toml at repo root):
  dir = ".claude/worktrees"                # where new worktrees are created

  [[worktree.copy]]                        # declarative file seed (before init)
    from = ".env.local"                      required; relative to primary root
                                             also the destination in the new worktree
                                             file or directory (dir = recursive)
                                             missing source → skipped
                                             existing destination → skipped (no overwrite)
                                             symlinks preserved as symlinks

  [[worktree.init]]                        # imperative post-create commands
    command = ["pnpm", "install"]            required; argv, shell is NOT involved
    dir     = "webapp"                       optional; relative to worktree root

ORDER after 'create':
  git worktree add → DB fork → copy → init (fail-fast, worktree kept on failure)
  'adopt' runs the same steps minus 'git worktree add'.

ENV passed to each init step:
  DX_WORKTREE_BRANCH  new branch name
  DX_WORKTREE_PATH    new worktree absolute path
  DX_PRIMARY_ROOT     primary checkout absolute path
`

const raycastHelp = `dx raycast — install the bundled Raycast extension

USAGE:
  dx raycast <install|uninstall>

SUBCOMMANDS:
  install      Extract the embedded extension to $XDG_DATA_HOME/dx/raycast-extension
               (default ~/.local/share/dx/raycast-extension), run npm ci, and import
               it into Raycast (one-shot ray develop; the extension persists).
               Re-run after upgrading dx to update the extension.
  uninstall    Remove the extracted directory. The Raycast entry itself must be
               removed in the Raycast UI (select extension → ⌘K → Remove Extension).

REQUIREMENTS:
  npm on PATH (Node.js — same prerequisite as portless), Raycast.app running.
`

const versionHelp = `dx version — print the CLI version

USAGE:
  dx version
`

const updateHelp = `dx update — self-update to the latest GitHub release

USAGE:
  dx update [--force]

FLAGS:
  --force, -f    also overwrite non-release ("dev") builds

NOTES:
  Downloads dx-<os>-<arch> from the latest release, verifies it against the
  release's SHA256SUMS, and atomically replaces the running binary.
  mise-managed installs refuse (even with --force) — use:
    mise up "github:agarichan/dx"
`

// helpFor returns command-specific help text, falling back to the overview helpText
// for unknown commands.
func helpFor(cmd string) string {
	switch cmd {
	case "logs":
		return logsHelp
	case "serve":
		return serveHelp
	case "up":
		return upHelp
	case "down":
		return downHelp
	case "status":
		return statusHelp
	case "stop":
		return stopHelp
	case "db":
		return dbHelp
	case "exec":
		return execHelp
	case "worktree", "wt":
		return worktreeHelp
	case "raycast":
		return raycastHelp
	case "update":
		return updateHelp
	case "version":
		return versionHelp
	default:
		return helpText
	}
}

// Run dispatches a subcommand and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, helpText)
		return 2
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, helpText)
		return 0
	}
	// `-h`/`--help` after any subcommand prints per-command help (e.g. `dx logs -h`).
	for _, a := range args[1:] {
		if a == "-h" || a == "--help" {
			fmt.Fprint(stdout, helpFor(args[0]))
			return 0
		}
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "dx %s\n", version)
		return 0
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "db":
		return runDB(args[1:], stdout, stderr)
	case "exec":
		return runExec(args[1:], stdout, stderr)
	case "up":
		return runUp(stdout, stderr)
	case "down":
		return runDown(stdout, stderr)
	case "logs":
		return runLogs(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "stop":
		return runStop(args[1:], stdout, stderr)
	case "worktree", "wt":
		return runWorktree(args[1:], stdout, stderr)
	case "raycast":
		return runRaycast(args[1:], stdout, stderr)
	case "update":
		return runUpdate(args[1:], stdout, stderr)
	case "__logpump": // internal: log fan-out pump (not in usage)
		return runLogPump(args[1:])
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		return 2
	}
}
