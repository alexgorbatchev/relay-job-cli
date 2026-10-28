---
created_on: 2026-10-25 05:51
last_modified: 2026-10-28 09:32
status: current
---

# relay-jobs-cli

Generic Linux/macOS batch runner. Executable: `relay-jobs`.

## Development

- Use Go 1.27.3+, OpenSSH, rsync and just. `just setup` downloads module dependencies; it does not install system packages.
- `just build` writes `bin/relay-jobs`; `just run` and `just run-ai` invoke the CLI.
- `just check` runs module hygiene, vet and race-enabled tests, enforcing 90% combined statement coverage. Change behavioral tests whenever behavior changes. Use real temporary files, sockets and native supervisors.
- Linux lifecycle tests require an active systemd user manager. macOS native tests require the launchd user domain. CI verifies the Linux manager before testing. Permission tests skip when running as root.
- Real SSH tests use `RELAY_JOBS_SSH_TEST_HOST` and `RELAY_JOBS_SSH_TEST_SOCKET`; point them at an isolated matching worker. Cross-compiled tests accept `RELAY_JOBS_TEST_BINARY`.

## Contracts

- Public configuration is strict TOML; durable receipts and Unix-socket APIs use JSON. Do not add JSON configuration compatibility layers.
- TOML paths resolve relative to the config file, with leading `~` expansion. CLI paths resolve from the caller's working directory. CLI flags override TOML. Service installation persists only explicit CLI overrides so TOML is reloaded on restart.
- Coordinator and worker defaults use separate state/cache directories and sockets. Never reuse daemon state for different instances.
- Persist assignment before submission. Reconcile ambiguous replies on the same worker and ID; never replay elsewhere or fall back after start submission.
- Request equality is exhaustive. Nil and empty collections compare equally after JSON omission.
- Reservations cover sync and execution; a native lock also covers rsync receivers. Preserve excluded dependency directories during checksum synchronization.
- Keep file writes atomic and durable, original stdout/stderr bytes separate, collection batches bounded, and workspace release after process cleanup.
- Linux uses systemd execution units; macOS uses separate launchd supervisor and command jobs. Supervisor loss stops descendants and produces an explicit lost result.
- Parse only direct job properties in native launchctl status dumps. Nested coalition/environment dictionaries reuse `state` and exit-status keys; their values must never override the job. Preserve annotated sysexits values such as `78: EX_CONFIG` as the numeric command status.
- Retention preserves receipts, execution markers and lock files indefinitely. Worker logs require durable collection acknowledgment. Mark logs expired before deleting streams; never delete active/locked workspaces or a local source checkout.
- Commands are trusted user workloads, without a sandbox, TTY, interactive stdin, or implicit environment forwarding. Source reads are not snapshots.

## CLI and documentation

- Use Cobra with cobra-help-tree, positional-argument metadata, bounded human help, compact agent help, and raw `--version` output.
- Canonical usage skill: `skills/relay-jobs/SKILL.md`. `relay-jobs skill` prints its GitHub installation command and URL identically in both modes, offline. Never embed or duplicate the skill.
- Update README, examples, usage skill and tests in the same change when commands, flags, defaults, TOML fields, output or side effects change. Update documentation timestamps.
- Preserve home-directory folding in displayed paths. Keep subprocess and stored paths absolute.

## Releases

- CI runs on main and pull requests. Version tags run verification before GoReleaser publishes Linux/macOS amd64/arm64 archives and checksums.
- Add `docs/releases/<tag>.md` before tagging. Release notes describe shipped behavior, without generated commit lists.
- Keep GoReleaser changelog processing enabled: `--release-notes` supplies the curated text, while `changelog.disable` would discard it.
- Build tagged artifacts locally with `goreleaser release --clean --skip=publish --release-notes docs/releases/<tag>.md`. Inspect archives and verify checksums before publishing.
- Publishing requires explicit user authorization. Keep binaries, caches, coverage, job state, credentials and environment files out of Git.
- Never publish private internal domains, host identities, network addresses or source history. Use generic examples and a fresh repository history.
