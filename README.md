`relay-jobs` runs tests, builds, and other batch commands on persistent Linux and macOS workers. It keeps dependencies and build caches warm, transfers changed source files, and gives callers durable job IDs, original output, and the command's exit status.

# What It Does

- **Reuse remote workspaces:** Synchronize dirty and untracked source, deletions, and file modes while preserving configured dependency directories.
- **Schedule across workers:** Select by worker name, operating-system label, required tools, warmth, and free slots.
- **Recover execution:** Reconnect after a lost reply or daemon restart using the same worker and job ID.
- **Inspect jobs:** List durable receipts and read elapsed duration, state, worker, and exit status.
- **Configure with TOML:** Define workers and service settings in user configuration, and reusable commands in project configuration.
- **Retain safely:** Expire old collected logs and unused workspaces while keeping active work and duplicate-execution protection.

# How It Works

- Install the binary on the submitting machine and every worker.
- Start a worker agent on each execution machine, and a coordinator on the submitting machine.
- Add worker SSH aliases, absolute socket paths, and optional labels to the coordinator's TOML file.
- Run `relay-jobs job exec -- <command...>` or define named jobs in `relay-jobs.toml` and run `relay-jobs job run <name>`.
- Follow output to completion, or submit with `--detach` and reconnect later.

```mermaid
flowchart TB
  subgraph execution[Each job]
    direction LR
    CLI[Your command] --> C[Coordinator: save ID<br/>and choose worker]
    C --> S[Sync changed source<br/>Reuse dependencies]
    S --> W[Worker runs command<br/>systemd or launchd]
    W --> O[Stream and persist output<br/>Record exit status]
    O --> ACK[Acknowledge<br/>durable collection]
  end
  subgraph retention[Independent maintenance: startup and hourly]
    TIMER[Cleanup timer] --> LOGS{Finished + 7 days old?<br/>Locks free?<br/>Worker copy acknowledged?}
    LOGS -->|Yes| EXPIRE[Expire stdout and stderr]
    LOGS -->|No| KEEP[Keep and retry later]
    TIMER --> CACHE{Workspace idle 30 days?<br/>All jobs terminal?<br/>No execution or sync lock?}
    CACHE -->|Yes| REMOVE[Rename, then delete cached workspace]
    CACHE -->|No| KEEP
  end
```

# How it Really Works

- The CLI is short-lived. Coordinator and worker agents remain running. The coordinator reaches private worker Unix sockets through reusable [OpenSSH](https://www.openssh.com/) connections; workers expose no TCP listener.
- Each canonical source directory gets its own workspace. [Rsync](https://www.samba.org/ftp/rsync/rsync.1) checksums both copies and transfers changed data. Checksumming still reads unchanged files. `.git/` is excluded automatically; specify dependency/cache exclusions explicitly.
- Linux uses native systemd user execution units; macOS uses launchd jobs. Agent or coordinator restarts preserve independent execution. An ambiguous start reply is reconciled on its assigned worker, without replaying elsewhere.
- Busy eligible workers queue jobs. With all eligible workers offline, the default is an explicit failure. `--unavailable local` opts into running the original checkout before remote submission. Configuration errors, source-sync failures, and command failures never trigger fallback.
- Command stdout/stderr stay separate original byte streams, with no cross-stream ordering guarantee. The generated job ID and CLI diagnostics go to stderr. `--quiet` omits the ID prefix. Human job receipts use labeled text and worker listings use tables; `AGENT=1` selects compact JSON receipts/listings and compact help and diagnostics. Command bytes remain unchanged. Displayed home paths use `~`; stored and subprocess paths stay absolute.
- Exit status matches the command. Cancellation returns 130; infrastructure failure or lost execution returns 125; CLI/API errors return 1. A missing execution unit without a completion receipt is reported as lost and is not replayed.
- State and mirrored logs live under `$XDG_STATE_HOME/relay-jobs` and workspaces under `$XDG_CACHE_HOME/relay-jobs`. Without overrides, worker agents use separate `relay-jobs-worker` directories. Bases default to `~/.local/state` and `~/.cache`.
- Maintenance runs at startup and hourly: completed logs expire after seven days; tracked idle workspaces after thirty days. Worker logs require durable collection acknowledgment. Receipts, execution markers, and lock files stay indefinitely. Expired log requests fail explicitly with HTTP 410. Workspace deletion includes excluded dependencies inside it; bootstrap them again on the next job.
- Jobs execute with the worker account's permissions and explicitly configured job environment. They are trusted workloads, without a sandbox, TTY, or interactive stdin. Keep source files stable during sync; reads are not a filesystem snapshot.

# Installation

Download the versioned archive for your operating system and CPU from the [latest release](https://github.com/alexgorbatchev/relay-jobs-cli/releases/latest). For an Apple silicon Mac:

```sh
curl -fLO https://github.com/alexgorbatchev/relay-jobs-cli/releases/download/v1.0.0/relay-jobs_1.0.0_darwin_arm64.tar.gz
tar -xzf relay-jobs_1.0.0_darwin_arm64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 755 relay-jobs "$HOME/.local/bin/relay-jobs"
```

Add `~/.local/bin` to PATH. Releases include Linux and macOS archives for amd64 and arm64, example configurations, and `checksums.txt`.

# Setup

- Configure SSH aliases, existing authentication, and trusted host keys for remote workers. An omitted worker `host` selects a local Unix socket.
- Install [rsync](https://rsync.samba.org) 3.0.0 or newer on coordinators and workers. macOS bundles openrsync, which lacks the protected-argument and delayed-deletion options source synchronization uses; install Homebrew's `rsync` there. Install each job's runtime and dependencies on its workers; the CLI binary itself does not require Go.
- `coordinator serve` and `worker serve` check rsync before serving and prepend `$XDG_DATA_HOME/relay-jobs/bin` (default `~/.local/share/relay-jobs/bin`) to their PATH. From an interactive terminal they offer to install a missing or outdated rsync with Homebrew, apt-get, pacman, or dnf; services and `AGENT=1` exit with the detected version instead. The worker records the absolute path of the rsync it verified, and SSH source synchronization runs that executable on the worker regardless of the SSH command PATH.
- Linux execution requires a running [systemd user manager](https://www.freedesktop.org/software/systemd/man/latest/systemd-run.html). Run `relay-jobs service install --role worker` on workers and `relay-jobs service install --role coordinator` on the submitting machine. The installer starts the service; restart an already active service after reinstalling its binary. Configure lingering separately when services must survive logout.
- macOS execution requires the native [launchd](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html) user domain and `plutil`. Run `relay-jobs worker serve`, or adapt [the LaunchAgent example](examples/launchd-worker.plist) with the absolute binary path and required tool PATH, save it as `~/Library/LaunchAgents/com.relay-jobs.worker.plist`, and load it with `launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.relay-jobs.worker.plist"`. This example starts at GUI login; it does not provision a system boot daemon. `service install` is Linux-only.
- Different daemon instances need distinct state, cache, and socket paths. For mixed operating systems, select a worker or OS label and exclude host-built dependencies before synchronization.

# Quick Start

On a worker, write `~/.config/relay-jobs/relay-jobs.toml`:

```toml
[worker]
slots = 2

[retention]
logs = "168h"
workspaces = "720h"
interval = "1h"
```

Start it with `relay-jobs worker serve`, or install the Linux worker service described above. Its default socket is `~/.local/state/relay-jobs-worker/worker.sock`.

On the submitting machine, write `~/.config/relay-jobs/relay-jobs.toml`, replacing the SSH alias and worker account's absolute socket path:

```toml
[[workers]]
name = "linux"
host = "build-worker"
socket = "/home/runner/.local/state/relay-jobs-worker/worker.sock"
labels = ["linux", "amd64"]
```

Start the coordinator in one terminal with `relay-jobs coordinator serve`, or install its Linux service. Then:

```sh
relay-jobs worker list
relay-jobs job exec --workers linux -- printf 'remote hello\n'
relay-jobs job list
```

Sample Output:

```text
job: 2124b1dc1116b9c466c94dee8c67560f
remote hello
```

Define a project `relay-jobs.toml`:

```toml
[jobs.test]
command = ["just", "test-local"]
workers = ["linux"]
requires = ["just"]
exclude = ["node_modules/", ".venv/", "bin/", ".tmp/"]
unavailable = "fail"

[jobs.test.env]
TEST_MODE = "integration"
```

Run `relay-jobs job run test`. The example uses a project's existing `just test-local` recipe; any executable and literal argument array is supported. Keep local commands separate from a remote wrapper to avoid recursion. Bootstrap excluded dependency directories from lockfiles with an ordinary remote job before testing.

# Options & Flags

Global flags:

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--config <path>` | — | `~/.config/relay-jobs/relay-jobs.toml` | Strict TOML user config; honors XDG_CONFIG_HOME |
| `--socket <path>` | — | `<effective-state>/coordinator.sock` | `[paths] socket` when set; worker default is `<effective-state>/worker.sock` |
| `--help` | `-h` | `false` | Command help |
| `--version` | `-v` | `false` | Raw version and newline |

Environment variables:

| Variable | Effect |
| :--- | :--- |
| `AGENT` | `1`, `true`, or `yes` selects agent mode |
| `COLUMNS` | Positive human help width; otherwise the terminal width, or 80 columns |
| `HOME` | Base of XDG fallbacks and leading `~` in TOML paths; folded to `~` in output |
| `XDG_CONFIG_HOME` | Base of the default config (`~/.config`) and of `service install` units |
| `XDG_STATE_HOME` | Base of the default state directories and sockets (`~/.local/state`) |
| `XDG_CACHE_HOME` | Base of the default workspace caches (`~/.cache`) |
| `XDG_DATA_HOME` | Base of `relay-jobs/bin`, holding managed rsync and the installed service binary (`~/.local/share`) |
| `PATH` | Tools and commands for serve commands and execution units; captured by `service install` |
| `XDG_RUNTIME_DIR`, `TMPDIR` | Coordinator SSH connection sockets, falling back to `TMPDIR`, then `/tmp` |

Help lists each command's variables. Jobs never inherit the client's environment; pass `--env`.

CLI flags override TOML, and help names the TOML key that supplies each default. Missing default configuration is optional for commands that do not need a worker list; an explicitly selected missing file fails. Unknown TOML keys fail. Relative TOML paths resolve from the configuration file and expand a leading `~`; CLI paths resolve from the caller's directory.

`relay-jobs worker serve`, `relay-jobs coordinator serve`, and `relay-jobs service install`:

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--state <path>` | — | `~/.local/state/relay-jobs` | Durable state; `[paths] state` when set; worker default is `~/.local/state/relay-jobs-worker` |
| `--cache <path>` | — | `~/.cache/relay-jobs` | Workspace cache; `[paths] cache` when set; worker default is `~/.cache/relay-jobs-worker` |

`worker serve` shows the worker directories as its defaults. `service install` chooses by `--role`, so its help names both.

`relay-jobs worker list`, `relay-jobs coordinator serve`, and `relay-jobs service install --role coordinator`:

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--worker-timeout <duration>` | — | `1s` | Positive availability budget per worker, including SSH setup and health response |

Set `[coordinator] worker_timeout = "1s"` in user TOML to configure the availability budget for scheduling and listing. `--worker-timeout` overrides it; explicit coordinator service installation flags persist across restarts. This budget covers waiting for a connection, SSH setup and the health response, rather than command execution. `worker list` probes concurrently, preserves configuration order and shows worker Status in human mode (`ready`, `working`, `unreachable`, `incompatible`, or `unavailable`) and probe failures in each entry's `error` field in agent mode. Human Slots show occupied/total (`1/2`); failed probes show `—` for unknown values and include their diagnostic in Status. Warm shows `—` because listing has no source workspace to assess. An offline worker does not fail the listing; coordinator/API failures still exit nonzero. The CLI allows two additional seconds for the coordinator response. A listing override applies to that request; scheduling uses the running coordinator's settings.

`relay-jobs job exec -- <command...>` and `relay-jobs job run <name>`:

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--source <path>` | — | `.` | Source directory, including dirty and untracked files |
| `--id <id>` | — | empty | Reuse a generated ID with the identical request |
| `--unavailable <policy>` | — | `fail` | `fail` or explicit `local` before submission |
| `--detach` | — | `false` | Submit without following output |
| `--quiet` | — | `false` | Omit the generated ID prefix on stderr |
| `--workers <names>` | — | `[]` | Comma-separated eligible worker names |
| `--exclude <patterns>` | — | `[]` | Rsync exclusions protected from deletion |
| `--requires <tools>` | — | `[]` | Required worker executables |
| `--labels <labels>` | — | `[]` | Required worker labels |
| `--env <KEY=VALUE>` | — | `[]` | Repeat for explicit command environment |

`relay-jobs job run <name>` only:

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--project <file>` | — | `relay-jobs.toml` | Named-job TOML relative to source |

`relay-jobs job logs <id>`:

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--follow` | — | `false` | Follow output and return command exit status |

`relay-jobs worker serve`:

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--slots <count>` | — | `1` | Positive simultaneous reservation/execution limit |

`relay-jobs worker serve`, `relay-jobs coordinator serve`, and `relay-jobs service install`:

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--log-retention <duration>` | — | `168h0m0s` | Completed-log lifetime; `0` disables expiry |
| `--workspace-retention <duration>` | — | `720h0m0s` | Idle workspace lifetime; `0` disables expiry |
| `--cleanup-interval <duration>` | — | `1h0m0s` | Positive maintenance interval |

`relay-jobs service install`:

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--role <role>` | — | empty | Required: `worker` or `coordinator` |
| `--slots <count>` | — | `1` | Positive worker concurrency limit; worker role only |
| `--name <unit.service>` | — | empty | Runtime default `relay-jobs-<role>.service` |
| `--binary <path>` | — | empty | Runtime default: the running executable |

The installer atomically replaces the binary in `$XDG_DATA_HOME/relay-jobs/bin/relay-jobs` and the unit file, syncing file contents and directory entries before starting the service, and captures PATH. On restart, services reread TOML; explicit installation flags remain overrides. User units go under `$XDG_CONFIG_HOME/systemd/user`. Bases default to `~/.local/share` and `~/.config`.

# TOML Configuration

The complete [user example](examples/relay-jobs-user.toml) configures workers, slots and retention. Optional local daemon paths:

```toml
[paths]
state = "state"
cache = "cache"
socket = "coordinator.sock"
```

Those relative paths resolve beside the config file. A custom state path also determines the default socket when `paths.socket` and `--socket` are absent. Use distinct files or overrides when hosting a worker and coordinator on the same machine.

Retention uses Go duration strings such as `24h` or `30m`, not `7d`. Periods must be nonnegative; interval must be positive. Named project jobs support `command`, `cwd`, `env`, `exclude`, `requires`, `workers`, `labels`, and `unavailable`. `cwd` must stay inside the source directory. CLI overrides replace the corresponding project settings.

# Recovery

Run `relay-jobs job list` to list all coordinator receipts, newest submission first (ties sort by ID). Listing reads durable state without contacting workers. An empty list prints `No jobs.` in human mode or `[]` in agent mode. Receipt read errors fail the command.

Keep the printed job ID. Query `relay-jobs job status <id>`, replay `relay-jobs job logs <id>`, or reconnect with `relay-jobs job logs <id> --follow`. Cancel with `relay-jobs job cancel <id>`. SIGINT/SIGTERM/SIGHUP while following requests cancellation. An unreachable worker leaves cancellation pending until recovery; do not submit another copy merely because a reply was lost.

Receipts include ID, state, worker, duration, exit code, optional error, and log-expiry status. Duration includes queueing, synchronization, and execution from submission until the terminal receipt update, or until the query for active jobs. It is a Go duration string rounded to milliseconds, such as `1.25s`. Human output uses labeled fields; agent output uses a JSON object for one receipt or an array for `job list`.

Invalid commands and output-write failures exit nonzero. Human help follows the terminal width or `COLUMNS`, falling back to 80 columns for redirected output; agent help stays untruncated.

The [agent usage skill](skills/relay-jobs/SKILL.md) covers the full command interface. `relay-jobs skill` prints its installation command and URL offline.

# License

[MIT](LICENSE), copyright Alex Gorbatchev.
