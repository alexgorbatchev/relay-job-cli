---
created_on: 2026-10-25 05:51
last_modified: 2026-10-29 15:31
status: current
---

# relay-jobs-cli

Generic Linux/macOS batch runner. Executable: `relay-jobs`.

## Development

- Use Go 1.27.3+, OpenSSH, rsync 3.0.0+ and just. On macOS install Homebrew `rsync`; the bundled openrsync lacks `--protect-args`. `just setup` downloads module dependencies; it does not install system packages.
- `just build` writes `bin/relay-jobs`; `just run` and `just run-ai` invoke the CLI.
- `just check` runs module hygiene, vet and race-enabled tests, enforcing 90% combined statement coverage. Change behavioral tests whenever behavior changes. Use real temporary files, sockets and native supervisors.
- Linux lifecycle tests require an active systemd user manager. macOS native tests require the launchd user domain. Gate backend-agnostic integration tests on the native manager (systemd on Linux, launchd on macOS), not on systemd alone. Keep platform-only code and tests in `_linux`/`_darwin` files so `just check` meets 90% coverage on both platforms. CI verifies the Linux manager before testing. Permission tests skip when running as root.
- Real SSH tests use `RELAY_JOBS_SSH_TEST_HOST` and `RELAY_JOBS_SSH_TEST_SOCKET`; point them at an isolated matching worker. Cross-compiled tests accept `RELAY_JOBS_TEST_BINARY`.

## Contracts

- Public configuration is strict TOML; durable receipts and Unix-socket APIs use JSON. Do not add JSON configuration compatibility layers.
- TOML paths resolve relative to the config file, with leading `~` expansion. CLI paths resolve from the caller's working directory. CLI flags override TOML. Service installation persists only explicit CLI overrides so TOML is reloaded on restart.
- Coordinator and worker defaults use separate state/cache directories and sockets. Never reuse daemon state for different instances.
- Probe worker availability concurrently within the positive `--worker-timeout` budget (default 1s, TOML `[coordinator] worker_timeout`), including connection-lock wait, SSH setup and health response. Listing preserves configuration order and returns per-worker errors; its client deadline must outlast the probe budget. Persist explicit timeout installation overrides and reread TOML on restart.
- Persist assignment before submission. Reconcile ambiguous replies on the same worker and ID; never replay elsewhere or fall back after start submission.
- Request equality is exhaustive. Nil and empty collections compare equally after JSON omission.
- Reservations cover sync and execution; a native lock also covers rsync receivers. Preserve excluded dependency directories during checksum synchronization.
- Keep file writes atomic and durable, original stdout/stderr bytes separate, collection batches bounded, and workspace release after process cleanup.
- Linux uses systemd execution units; macOS uses separate launchd supervisor and command jobs with `ProcessType` `Standard`; `Background` lets macOS delay and throttle user-requested commands on a busy worker. Supervisor loss stops descendants and produces an explicit lost result.
- Stopping execution returns only after the supervisor exits. `launchctl bootout` returns early, so macOS waits for the supervisor's per-job `execution.lock`, held until its workspace lock and final receipt are released.
- Declare external executables and minimum versions in `internal/deps` with godeps, and use godeps for agent-mode detection. Serve commands call godeps `Ensure`. Workers record the verified absolute rsync in each reservation; the SSH sync receiver runs that path and never searches sshd's command PATH.
- Parse only direct job properties in native launchctl status dumps. Nested coalition/environment dictionaries reuse `state` and exit-status keys; their values must never override the job. Preserve annotated sysexits values such as `78: EX_CONFIG` as the numeric command status.
- Retention preserves receipts, execution markers and lock files indefinitely. Worker logs require durable collection acknowledgment. Mark logs expired before deleting streams; never delete active/locked workspaces or a local source checkout.
- Commands are trusted user workloads, without a sandbox, TTY, interactive stdin, or implicit environment forwarding. Source reads are not snapshots.

## CLI and documentation

- Use Cobra with `github.com/alexgorbatchev/cobra-help-tree/v3`, positional-argument metadata, human help clipped to the terminal width (80-column fallback), compact agent help, and raw `--version` output.
- Canonical usage skill: `skills/relay-jobs/SKILL.md`. `HelpOptions.Skill` (`skillSource` in `cmd/relay-jobs/help.go`) makes cobra-help-tree add the `skill` command, which prints the GitHub installation command and URL identically in both modes, offline, and opens every agent help screen with the skill alert; cobra-help-tree detects agent mode for help screens itself. Do not hand-roll the skill command or alert. Never embed or duplicate the skill.
- Update the usage skill in the same change as any command, argument, flag, default, environment variable, TOML field, output, or side-effect change; `cobrahelptree.SkillOmissions` tests fail on undocumented commands, flags, types, and defaults. Update README, examples and tests in the same change too, and update documentation timestamps.
- Help states effective defaults. Fixed-role commands register their role's `job.DefaultPaths`; runtime-resolved values (`--socket`, `service install` paths) register empty defaults and describe the resolution in usage, including the TOML key that supplies it. Declare every environment variable the CLI reads in `techCatalog` `Env`; tests compare help with `configure` and scan sources for reads.
- Only config/socket are global; state/cache belong to serve/install commands and worker-timeout to coordinator serve, worker list, and coordinator installation. Reject irrelevant role-specific installation flags.
- Human job receipts use labeled text; worker listings use go-pretty tables with Status and occupied/total Slots. Unknown health and unassessed workspace warmth display as `—`. Agent mode uses JSON. Job list reads coordinator receipts newest first, with ID tie-breaking. Receipt duration includes queueing, synchronization, and execution through terminal update or the current query.
- Preserve home-directory folding in displayed paths. Keep subprocess and stored paths absolute.

## Releases

- CI runs on main and pull requests. Version tags run verification before GoReleaser publishes Linux/macOS amd64/arm64 archives and checksums.
- Add `docs/releases/<tag>.md` before tagging. Release notes describe shipped behavior, without generated commit lists.
- Keep GoReleaser changelog processing enabled: `--release-notes` supplies the curated text, while `changelog.disable` would discard it.
- Build tagged artifacts locally with `goreleaser release --clean --skip=publish --release-notes docs/releases/<tag>.md`. Inspect archives and verify checksums before publishing.
- Publishing requires explicit user authorization. Keep binaries, caches, coverage, job state, credentials and environment files out of Git.
- Never publish private internal domains, host identities, network addresses or source history. Use generic examples and a fresh repository history.
