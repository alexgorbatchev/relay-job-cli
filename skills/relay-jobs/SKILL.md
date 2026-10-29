---
name: relay-jobs
description: Use when submitting remote commands, configuring workers, inspecting job receipts, recovering interrupted output, or installing relay-jobs user services.
author: alexgorbatchev
metadata:
  created_on: 2026-10-22 22:15
  last_modified: 2026-10-29 12:05
  status: current
---

Run batch commands in persistent Linux or macOS worker workspaces. Set `AGENT=1` for compact help, errors, and JSON receipts/listings. Human output uses labeled text. Human help follows terminal width or positive `COLUMNS`, with an 80-column fallback; agent help is untruncated. Output write failures and invalid commands exit nonzero. Displayed home paths use `~`; stored and subprocess paths stay absolute. Job stdout/stderr remain original bytes in both modes.

## `relay-jobs`

Invoke without arguments for help. Global flags apply to every command:

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--config` | string | `$XDG_CONFIG_HOME/relay-jobs/relay-jobs.toml` | User configuration; config base falls back to `~/.config`. |
| `--socket` | string | `<default-state>/coordinator.sock` | Runtime default follows effective state; workers use worker.sock. |
| `--help` | bool | false | Print command help; `-h`. |
| `--version` | bool | false | Print raw version and newline; `-v`. |

CLI flags override strict TOML user settings. Missing default configuration is optional except the coordinator needs workers; an explicitly selected missing config fails. Relative `[paths]` values resolve beside the config file and expand a leading `~`. CLI paths resolve from the caller's directory. The socket default follows effective state; worker serve and worker service install use separate `relay-jobs-worker` state/cache directories and `worker.sock`. Explicit paths override these defaults; keep different instances separate. Unix socket paths must fit the OS limit. Commands use current-user permissions without interactive stdin or TTY.

## `relay-jobs coordinator`

Manage the primary machine's coordinator.

## `relay-jobs coordinator serve`

Read the worker config and serve until SIGINT/SIGTERM/SIGHUP. Recover durable jobs by their existing IDs. Requires `ssh`, rsync 3.0.0 or newer, and the OS's native manager for local fallback. macOS's bundled openrsync is too old; use Homebrew's `rsync`.

Before serving, `coordinator serve` and `worker serve` prepend `$XDG_DATA_HOME/relay-jobs/bin` (default `~/.local/share/relay-jobs/bin`) to PATH and check rsync. An interactive terminal offers a `[y/N]` installation through Homebrew, apt-get, pacman, or dnf; declining, closed stdin, services, and `AGENT=1` exit 1 with the detected version. `AGENT` values `1`, `true`, and `yes` select agent mode. Workers record the absolute path of the verified rsync in each reservation, and the SSH sync receiver runs that executable regardless of the SSH command PATH.

Worker configuration:

```toml
[[workers]]
name = "build"
host = "build-worker"
socket = "/home/runner/.local/state/relay-jobs-worker/worker.sock"
labels = ["linux"]
```

Optional user tables: `[paths]` accepts `state`, `cache`, `socket`; `[worker]` accepts positive `slots`; `[coordinator]` accepts a positive `worker_timeout` duration string (default `"1s"`); `[retention]` accepts duration strings `logs`, `workspaces`, `interval`. They supply defaults for the corresponding serve/install flags. Unknown keys and invalid values fail. Services reread TOML on restart; explicit installation flags remain overrides.

Use SSH aliases with preconfigured keys and trusted host keys. An omitted `host` connects to a local worker socket, useful for additional local agents. Worker names are unique; `local` identifies fallback and is reserved. Configure multiple entries to enable scheduling. Prefer matching workers with warm workspaces, then lower occupancy. Busy workers queue jobs. Availability probes run concurrently within `--worker-timeout`, covering connection-lock wait, SSH setup and health response. This does not limit command execution. Authentication, host-key, protocol, and missing-tool errors require configuration repair.

For mixed operating systems, specify `--workers` or `--labels` on platform-dependent jobs. Without either filter, any configured worker is eligible. Exclude host-built dependencies and caches before synchronizing to another OS.

The following local flags apply to `coordinator serve`, `worker serve`, and `service install`, not client commands:

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--state` | string | `$XDG_STATE_HOME/relay-jobs` | Durable records; base falls back to `~/.local/state`. Workers default to relay-jobs-worker. |
| `--cache` | string | `$XDG_CACHE_HOME/relay-jobs` | Workspaces; base falls back to `~/.cache`. Workers default to relay-jobs-worker. |

`coordinator serve` also accepts:

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--worker-timeout` | duration | 1s | Positive coordinator availability budget, including SSH setup and health response. |

Configure automatic cleanup on `coordinator serve`, `worker serve`, or `service install`:

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--log-retention` | duration | 168h0m0s | Expire completed-job logs after seven days; 0 disables expiry. |
| `--workspace-retention` | duration | 720h0m0s | Delete tracked worker workspaces after thirty idle days; 0 disables expiry. |
| `--cleanup-interval` | duration | 1h0m0s | Positive interval between cleanup passes; also run at service startup. |

Use Go durations such as `24h` or `30m`, not `7d`. Both retention periods must be nonnegative. Each remote worker owns its workspace policy; the coordinator never deletes fallback's original checkout. Worker logs remain until the coordinator acknowledges durable collection. Active or unresolved jobs and held workspace locks protect data. Keep receipts, `started` markers, and lock files indefinitely: they prevent replay of an old ID. Expired log requests fail with HTTP 410; status and same-ID replay protection remain available. Workspace removal includes preserved dependency directories, so bootstrap dependencies on the next job. Untracked directories and workspace symlinks are not removed.

## `relay-jobs worker`

Manage worker agents.

## `relay-jobs worker serve`

Serve a private worker API. Supply distinct `--socket`, `--state`, and `--cache` when hosting multiple agents on one machine. Linux requires `systemd-run`, `systemctl` and a running user manager. macOS requires native `launchctl`, `plutil` and a user domain. Both require rsync 3.0.0 or newer, checked at startup as for `coordinator serve`, and the job's toolchain. Install workspace dependencies from lockfiles or through a bootstrap job.

On macOS, commands and execution supervisors have separate native launchd jobs. Agent restarts preserve execution; supervisor loss stops the command and reports a lost receipt.

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--state` | string | `$XDG_STATE_HOME/relay-jobs` | Durable records; base falls back to `~/.local/state`. Workers default to relay-jobs-worker. |
| `--cache` | string | `$XDG_CACHE_HOME/relay-jobs` | Workspaces; base falls back to `~/.cache`. Workers default to relay-jobs-worker. |
| `--slots` | int | 1 | Positive simultaneous reservation/execution limit. |
| `--log-retention` | duration | 168h0m0s | Expire completed logs after seven days; 0 disables expiry. |
| `--workspace-retention` | duration | 720h0m0s | Delete tracked workspaces after thirty idle days; 0 disables expiry. |
| `--cleanup-interval` | duration | 1h0m0s | Positive interval; also clean at startup. |

## `relay-jobs worker list`

Ask the coordinator for each configured worker's health, occupancy, protocol, and connection error. Print labeled worker entries in configuration order, or JSON with `AGENT=1`; inspect each entry's error. Probes run concurrently; unavailable workers are per-entry errors and do not fail the command. Coordinator/API failures exit nonzero. `--worker-timeout` overrides TOML for this request; the client allows two additional seconds to receive the response. Scheduling uses the running coordinator's settings. Checking health creates reusable SSH tunnels.

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--worker-timeout` | duration | 1s | Positive coordinator availability budget, including SSH setup and health response. |

## `relay-jobs job`

Submit and inspect durable jobs.

## `relay-jobs job exec -- <command...>`

Submit literal executable arguments after `--`. Generate an ID before submission and print `job: <id>` to stderr. Keep this ID if any reply is lost. Default strict mode fails when all eligible workers are unreachable. A command's nonzero exit code never triggers fallback. After a start request, recover that ID rather than replaying elsewhere.

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--source` | string | . | Source directory, including dirty and untracked files. |
| `--id` | string | empty | Reconcile/reuse a generated 32-character lowercase hex ID with the same request. |
| `--unavailable` | string | fail | `fail` or explicit `local` fallback before execution submission. |
| `--detach` | bool | false | Submit and print a receipt without following; JSON in agent mode. |
| `--quiet` | bool | false | Omit the job ID prefix from stderr; use `--id` when retaining an ID is necessary. |
| `--workers` | stringSlice | [] | Comma-separated eligible worker names. |
| `--exclude` | stringSlice | [] | Rsync patterns excluded from source and protected from deletion remotely. |
| `--requires` | stringSlice | [] | Required worker executables on PATH. |
| `--labels` | stringSlice | [] | Required worker labels. |
| `--env` | stringArray | [] | Repeat KEY=VALUE to set command environment. |

Synchronization checksums files, updates modes, and deletes removed source files. `.git/` is always excluded. Specify dependency/cache exclusions explicitly; do not copy host-built dependencies to another platform. Source reads must remain stable during synchronization; this tool does not freeze files edited concurrently. Each source directory has a distinct workspace identity. Worker reservation spans sync and execution.

Follow preserves separate binary stdout/stderr, with no cross-stream ordering guarantee. Exit status is the command's status, 130 for canceled jobs, 125 for infrastructure failure or lost execution, and 1 for CLI/API errors. SIGINT/SIGTERM/SIGHUP requests cancellation; an unconfirmed cancellation prints recovery instructions. Detached jobs continue when the client exits. Logs expire under the service's retention policy; receipts persist.

## `relay-jobs job run <name>`

Read the named job from `relay-jobs.toml` in the source directory. Accept the same flags as `job exec`; provided overrides replace corresponding job settings. Use separate local recipes to avoid recursive wrappers:

```toml
[jobs.test]
command = ["just", "test-local"]
workers = ["build"]
unavailable = "local"
exclude = ["node_modules/", ".venv/", "bin/", ".tmp/"]
requires = ["just"]
```

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--project` | string | relay-jobs.toml | Named-job file relative to source; only accepted by job run. |

Additional job fields: `env` is a string map; `cwd` is a relative directory inside the synchronized workspace; `labels` and `requires` constrain scheduling. Local fallback uses the original source directory and requires its own toolchain/dependencies. Sync failures and configuration errors fail explicitly.

## `relay-jobs job list`

List the coordinator's durable receipts, newest submission first, with ID ascending for equal timestamps. This reads local coordinator records without probing workers. Empty output is `No jobs.` for humans or `[]` in agent mode. API and corrupt-record errors exit nonzero. Entries use the same fields as job status.

## `relay-jobs job status <id>`

Print the coordinator's durable receipt as labeled text, or JSON in agent mode, with `id`, `state`, `worker`, `duration`, `exit_code`, optional `error`, and `logs_expired: true` after cleanup. Duration includes queueing, synchronization, and execution from coordinator submission until the terminal receipt update, or until the query for active jobs. It is a Go duration string rounded to milliseconds. Job list returns an array of these objects. A worker outage after submission keeps the job pending reconciliation, with an explanatory error; it is not safe to start another copy. `lost` means the execution unit disappeared without a completion receipt. Repair the worker connection and recheck before manually deciding to submit a new ID.

## `relay-jobs job logs <id>`

Replay saved stdout/stderr from the beginning.

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--follow` | bool | false | Continue until completion and return command status. |

## `relay-jobs job cancel <id>`

Cancel queued work or stop the worker's native execution jobs and their descendants. Cancellation completes after the execution supervisor exits and releases the workspace. Print the resulting receipt. An unreachable worker makes cancellation unconfirmed; reconnect and retry with the same ID.

## `relay-jobs service`

Manage reproducible user-service installation.

## `relay-jobs service install`

On Linux, copy the running binary into `$XDG_DATA_HOME/relay-jobs/bin/relay-jobs` (base defaults to `~/.local/share`), write `$XDG_CONFIG_HOME/systemd/user/relay-jobs-<role>.service`, reload the user manager, enable/start, and verify active status. Binary and unit replacements are atomic and synced durably. The unit captures PATH and explicit CLI overrides. Services reread TOML on restart. Restart an already active service with `systemctl --user restart relay-jobs-<role>` after reinstalling the binary. Inspect `loginctl show-user "$USER" -p Linger` if services must survive logout; enabling lingering is a separate host configuration action. This command rejects macOS before changing files; use the repository's `examples/launchd-worker.plist` as a LaunchAgent template with the correct absolute executable and tool PATH. That example starts at GUI login, not system boot.

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--state` | string | `$XDG_STATE_HOME/relay-jobs` | Durable records; base falls back to `~/.local/state`. Workers default to relay-jobs-worker. |
| `--cache` | string | `$XDG_CACHE_HOME/relay-jobs` | Workspaces; base falls back to `~/.cache`. Workers default to relay-jobs-worker. |
| `--worker-timeout` | duration | 1s | Coordinator role only; positive availability budget, including SSH setup and health response. |
| `--role` | string | empty | Required: coordinator or worker. |
| `--slots` | int | 1 | Positive worker concurrency limit; worker role only. |
| `--name` | string | empty | Custom service unit name ending in `.service`; defaults to `relay-jobs-<role>.service`. |
| `--binary` | string | empty | Binary to install; defaults to the running executable. |
| `--log-retention` | duration | 168h0m0s | Persist completed-log lifetime in the installed service; 0 disables expiry. |
| `--workspace-retention` | duration | 720h0m0s | Persist worker idle lifetime; 0 disables expiry. |
| `--cleanup-interval` | duration | 1h0m0s | Persist a positive cleanup interval. |

Choose an XDG data directory without quotes, backslashes, or control characters; systemd rejects those characters in an executable path. Spaces, percent signs, and literal dollar signs are supported. Service arguments and PATH values use systemd quoting with environment substitution disabled for the command line.

## `relay-jobs skill`

Print the Skills CLI installation command and canonical skill URL, identically in both modes, without network access. Read the URL using authenticated repository access when required.

## `relay-jobs help [command]`

Print help for a command path. All public commands accept `--help`/`-h`.

## `relay-jobs completion`

Print shell-completion help.

## `relay-jobs completion bash`

Print Bash completion. Accept `--no-descriptions` (bool, false).

## `relay-jobs completion zsh`

Print Zsh completion. Accept `--no-descriptions` (bool, false).

## `relay-jobs completion fish`

Print Fish completion. Accept `--no-descriptions` (bool, false).

## `relay-jobs completion powershell`

Print PowerShell completion. Accept `--no-descriptions` (bool, false).
