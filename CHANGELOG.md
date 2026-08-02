# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow SemVer
(`v0.x.y` while pre-stable).

## [Unreleased]

### Fixed

- The `install.sh` one-line installer no longer aborts when the OpenShell installer it invokes
  reports its gateway as unreachable. That health check cannot pass until the driver's `setup`
  runs afterwards, so its non-zero exit is expected; the installer now tolerates it (after
  confirming the `openshell` binary was installed) and continues on to install the driver and
  run `setup`, which brings the gateway up. Without this, a fresh `curl … | sh` left OpenShell
  and apple/container installed but the driver never set up.

## [0.2.3] - 2026-08-02

### Fixed

- Fixed a race where an old driver instance's deferred socket cleanup could delete a *newer*
  instance's live socket file if the old one was still draining in-flight RPCs (up to 10s)
  when the replacement bound the same path — e.g. during a fast restart or upgrade. The
  listener now claims ownership of its socket with a random per-process token recorded in a
  companion file at bind time, and only unlinks the socket at shutdown if that token still
  matches; otherwise it logs a warning and leaves the newer instance's socket in place. (#21)

### Added

- `--network-policy-file`: an operator-only flag that replaces the sandbox image's baked-in
  `/etc/openshell/policy.yaml` network policy for every sandbox, installed as root during boot
  before the supervisor starts. Lets an operator allowlist additional egress a tool's
  self-updater needs (e.g. `downloads.claude.ai` for `claude update`) without weakening the
  default policy for anything not explicitly added. Driver-wide and operator-only — never
  settable per sandbox. Verified live: `claude update` succeeds with an overlay adding the one
  missing host, while unrelated hosts remain blocked.

### Changed

- Replaced the ASCII architecture diagrams with Mermaid (topology, create flow, lifecycle, and
  reconcile) in the README, `docs/architecture.md`, and `docs/CONTRACT.md` — GitHub renders
  them natively.

## [0.2.2] - 2026-08-02

### Added

- One-line installer (`curl -LsSf …/install.sh | sh`): checks prerequisites (Apple silicon
  macOS, Homebrew, apple/container, OpenShell) and offers to install any that are missing, then
  downloads the driver release, verifies its checksum, and runs `setup`. Non-interactive with
  `-y`; supports `--no-setup`, `--version`, `--prefix`. Activates when the repo is public.
- CI `shellcheck` job lints the installer, e2e scripts, and git hooks.

### Security

- **Host volume mounts are now opt-in.** Per-sandbox `--driver-config-json` `volume` mounts are
  rejected unless the driver is started with `--allow-host-mounts`, and `--host-mount-root`
  constrains permitted source directories. `tmpfs` mounts are unaffected. (#11)
- **The per-sandbox network override is allowlisted** to `--network` plus `--allowed-networks`;
  a sandbox spec can no longer attach a VM to an arbitrary vmnet network. (#12)
- Enabled GitHub Dependabot (alerts, security updates, weekly version updates for Go modules
  and Actions) and the dependency graph.
- Added CI security scanning: govulncheck, gosec (SAST), and gitleaks (secret scan over full
  history), plus a CodeQL workflow that activates when the repo is public and a `make sec`
  target mirroring CI. Documented in SECURITY.md.
- Pinned all GitHub Actions to commit SHAs (kept current by Dependabot) and tightened workflow
  token permissions to least privilege.
- Hardened file permissions: seed material, the launchd plist, and driver-owned directories
  are now owner-only (0600/0700); the socket directory is checked for symlink/ownership.
- Bumped `golang.org/x/text` to v0.39.0 and `golang.org/x/net` to v0.56.0 (advisories
  GO-2026-5970, GO-2026-5942; neither was reachable from our code).

### Fixed

- `setup` reliably restarts the driver service right after a binary upgrade: it waits for
  launchd to fully unload the old instance before bootstrapping (fixing a transient
  `Bootstrap failed: 5` seen mid-drain), retries the bootstrap, and forces a `kickstart` if the
  service does not come up — failing loudly instead of leaving it silently down. (#14)

## [0.2.1] - 2026-08-02

### Added

- Open-source governance set: MAINTAINERS.md, CODEOWNERS, Contributor Covenant code of
  conduct, issue and pull-request templates.
- DCO sign-off is now required for contributions: `Signed-off-by` enforced on pull-request
  commits by a new `dco` CI job, added automatically by the repo's `prepare-commit-msg` hook,
  and applied to web-UI commits by a repository setting.

### Fixed

- Supervisor extraction is now concurrency-safe. Two concurrent first-time sandbox creates
  could previously race on a shared extraction-container name and temp file, failing one
  create or caching a truncated supervisor binary that every later sandbox would reuse.
- A canceled or short-deadline `DeleteSandbox` no longer leaves a half-removed sandbox that
  the poller skips and a restart re-adopts; teardown runs under a detached, bounded context.
- Hardened host-facing inputs surfaced by a pre-release audit: the socket directory is
  verified to be a non-symlink owned by the current user; `gateway.env` values are
  shell-quoted; endpoints that are unspecified addresses (`0.0.0.0` / `::`) and flag-like
  image references are rejected; the `gateway.env` managed-block parser tolerates CRLF and
  duplicate blocks.

## [0.2.0] - 2026-08-02

### Added

- **One-command installation**: `openshell-driver-applecontainer setup` wires the whole stack
  permanently — driver as a launchd service, the stock Homebrew gateway service configured
  through its `gateway.env` hook (driver selection, non-loopback bind, mTLS auth), gateway
  certificate SAN for the vmnet address (CA preserved), vmnet network creation, CLI
  registration repair, and image pre-pull. Idempotent; `uninstall` reverses it.
- Gateway endpoint auto-derivation: with `--grpc-endpoint` unset the driver resolves
  `https://<vmnet-gateway-ip>:17670` from the configured network at startup, so zero flags
  are needed.
- Guest TLS bundle auto-detection across the standard locations (`$OPENSHELL_LOCAL_TLS_DIR`,
  XDG state, Homebrew) and a container-runtime auto-start attempt when it is down.
- Guest console tail attached to exited-sandbox conditions and Warning events; startup
  preflight validation; driver-level `--kernel` default (from the risk-fix round).
- `make install` target.

## [0.1.0] - 2026-08-02

### Added

- OpenShell extension compute driver (`applecontainer`) implementing the full
  `openshell.compute.v1.ComputeDriver` contract (pinned to OpenShell v0.0.96) over a
  Unix socket with owner-only permissions.
- VM-per-sandbox provisioning on apple/container: supervisor extraction from the
  release-matched image (digest-keyed cache), per-sandbox read-only seed directory
  (supervisor, gateway TLS triple, 0600 per-sandbox JWT, boot shim), boot as guest root
  with the upstream capability set, workload privilege drop to the image's OCI user.
- Lifecycle: accept-then-provision create with watch progress events, delete with
  in-flight-create cancellation, startup reconcile (adopt running VMs, mark exited/missing,
  clean labeled orphans), 2 s runtime poller publishing state transitions.
- Images and resources: local-first resolution, digest pinning at inspect time, Kubernetes
  quantity mapping to real VM sizing, per-sandbox driver config (volume/tmpfs mounts with
  reserved-target enforcement, network override, custom kernel passthrough).
- Live acceptance on the reference machine: create→Ready mean 1.1 s over a 10-cycle soak,
  policy-forbidden egress blocked in-guest (HTTP 403), restart adoption, clean teardown.

[Unreleased]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.3...HEAD
[0.2.3]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/vyncint/openshell-driver-applecontainer/releases/tag/v0.1.0
