# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow SemVer
(`v0.x.y` while pre-stable).

## [Unreleased]

## [0.2.12] - 2026-08-22

### Fixed

- **`--openshell-version` never worked.** OpenShell tags every release `vX.Y.Z` and its installer
  uses `OPENSHELL_VERSION` verbatim as the tag in the asset URL, so the documented
  `--openshell-version 0.0.97` asked for a release that does not exist and the install died with
  "the selected release may not include a Homebrew formula". Both the flag (`update --all
  --openshell-version`) and `install.sh --openshell-version` now normalise a bare `X.Y.Z` to
  `vX.Y.Z`; an explicit `vX.Y.Z` and OpenShell's `dev` literal pass through untouched. Broken
  since the flag shipped in v0.2.7.

### Changed

- **Compatibility verified against OpenShell 0.0.111 and apple/container 1.2.2** (previously
  0.0.97 and 1.2.0), live on the reference machine: create → Ready → exec → delete, egress still
  blocked by policy, and restart adoption. The contract gained four RPCs after v0.0.96 that this
  driver does not implement; three are optional by design (the gateway maps `Unimplemented` to
  success) and `StopSandbox`/`StartSandbox` are reached only by the new
  `openshell sandbox stop`/`start` commands, which fail with a clear message. README, STATUS and
  `docs/CONTRACT.md` now spell this out rather than implying the v0.0.96 contract is current.

## [0.2.11] - 2026-08-17

### Security

- Build against Go 1.26.6. The `go` directive pinned 1.26.5, which CI installs via
  `go-version-file`, so every build shipped four standard-library vulnerabilities that Go fixed in
  1.26.6 and that `govulncheck` reported as reachable from our code: GO-2026-6218 (`net/url`),
  GO-2026-6090 (`crypto/tls`), GO-2026-5972 (`encoding/asn1`) and GO-2026-5026 (`net/http`). The
  driver terminates TLS to the gateway and parses URLs and certificates, so all four are on live
  paths. `govulncheck` is clean again.
- A weekly `go-toolchain` workflow now watches for new Go patch releases and files an issue when
  `go.mod` falls behind, so the next one is caught deliberately rather than as a surprise red
  `govulncheck` on an unrelated pull request. Dependabot cannot do this — bumping the Go directive
  is still an open request upstream (dependabot/dependabot-core#13520) — which is now recorded in
  `dependabot.yml` so nobody adds an inert stanza for it.

### Changed

- `google.golang.org/protobuf` 1.36.11 → 1.36.12 (upstream bug fixes; the added `prototext`
  recursion limit and `protodelim` size check do not affect this driver, which speaks the binary
  wire format over gRPC).

## [0.2.10] - 2026-08-08

### Fixed

- Fixed a flaky `TestPollerTracksExit` (#38). `provision` sets the sandbox's terminal condition
  and only then returns, and the entry's `done` channel is closed after that — so a test that
  waited for `BackendReady` and immediately drove `pollOnce` could land in the window where the
  entry still counted as in flight, which `pollOnce` skips by design. No transition happened and
  the test timed out. The affected tests now wait for the provisioning task itself; the same
  window also let `TestPollerKeepsProvisioningFailure` pass for the wrong reason. Test-only —
  in production the next 2 s poll covers the window.

## [0.2.9] - 2026-08-08

### Fixed

- `setup` no longer pins the launchd service to a Homebrew version directory. It resolved symlinks
  before writing the plist, so on a cask install the service pointed at
  `Caskroom/openshell-driver-applecontainer/<version>/…`, which `brew upgrade` deletes. The plist
  now keeps Homebrew's `<prefix>/bin` symlink, which is stable across versions; every other
  symlink is still resolved, so the plist does not depend on one staying put.

### Changed

- Documented that **`brew upgrade` removes the driver's launchd service**, so `setup` must follow
  it. Homebrew replaces a cask by uninstalling the old version first, which runs the cask's
  `uninstall launchctl:` directive — Homebrew skips only `signal` on upgrade. Keeping the
  directive is deliberate: without it a real `brew uninstall` would leave a loaded agent
  respawning a binary that no longer exists.

## [0.2.8] - 2026-08-08

### Added

- **Homebrew install**:
  `brew install nvidia/openshell/openshell vyncint/tap/openshell-driver-applecontainer`. Every
  release now publishes a cask to [vyncint/homebrew-tap](https://github.com/vyncint/homebrew-tap) —
  a cask rather than a formula because Homebrew treats pre-built binaries that way, and because a
  cask can strip the quarantine bit from our unsigned binary. It unloads the driver's launchd agent
  on `brew uninstall` and removes the driver's state, plist and log on `--zap`; pre-releases are not
  published to the tap. OpenShell is declared as a dependency but is worth naming on the command
  line anyway, since Homebrew auto-taps only the names it is given and would otherwise resolve the
  dependency to nothing on a host without the `nvidia/openshell` tap. apple/container stays outside
  Homebrew's reach entirely (signed `.pkg`) and is covered by the caveats.
- `install.sh` and the manual path are unchanged and still the way to install everything —
  including apple/container — in one step.

### Fixed

- `update` no longer corrupts a Homebrew installation. It resolves symlinks to find the running
  binary, which for a cask points inside `Caskroom/<token>/<version>/`, so replacing it in place
  left Homebrew convinced it still had the version it staged (and a later `brew upgrade` or
  `uninstall` acting on the wrong files). It now detects a cask install, delegates to
  `brew upgrade --cask`, and re-runs `setup` through the Homebrew symlink rather than the
  now-replaced version directory. `--version` is rejected for cask installs, which can only track
  the tap's latest release.

## [0.2.7] - 2026-08-05

### Fixed

- **The supervisor image tag now tracks the installed gateway.** It was hardcoded to `0.0.96`
  while the OpenShell installer resolves its own latest release, so a host running gateway
  0.0.97 booted every sandbox with a 0.0.96 supervisor — a silent version mismatch, since the
  supervisor runs inside the sandbox and speaks to the gateway. The driver now reads
  `openshell-gateway --version` and uses the matching `supervisor:<version>`; `--supervisor-image`
  (or `OSHL_AC_SUPERVISOR_IMAGE`) still pins it explicitly, and an unpublished matching tag falls
  back to the pinned one instead of failing every create.

### Added

- Version pinning for the prerequisites, for reproducible installs and upstream rollbacks:
  `install.sh --openshell-version X.Y.Z --container-version X.Y.Z` (env
  `OSHL_AC_OPENSHELL_VERSION`, `OSHL_AC_CONTAINER_VERSION`) and
  `update --all --openshell-version … --container-version …`. Previously only the driver's own
  release was selectable; OpenShell and apple/container always resolved to latest. OpenShell
  pinning goes through its official installer (which honors `OPENSHELL_VERSION`), apple/container
  through `update-container.sh -v`.
- `setup` reports the resolved driver / gateway / apple-container versions and the supervisor
  image, and warns when the supervisor tag does not match the gateway.

## [0.2.6] - 2026-08-02

### Fixed

- `setup` now installs apple/container's recommended guest kernel when no default is configured.
  apple/container cannot boot any VM without one, so on a fresh install — or one whose user data
  was deleted, which `cleanup --all -d` does by design — every sandbox create failed at image
  unpack with `default kernel not configured for architecture arm64`. The step is skipped when a
  kernel is already set, and a failed download only warns (with the retry command) so `setup`
  still repairs the rest of the wiring.
- `update --all` now stops the container runtime before running apple/container's updater and
  restarts it afterwards. The updater refuses to run while the runtime is up, so this step
  previously always failed with "`container` is still running".

## [0.2.5] - 2026-08-02

### Fixed

- `update` now keeps the replaced binary executable. It staged the new binary in a temp file
  created with mode 0600 and relied on `OpenFile` to widen it, but `OpenFile` ignores the mode
  argument for a file that already exists — so the installed binary was left non-executable and
  the follow-up `setup` failed with "permission denied". The copy now forces the mode
  explicitly. (regression in v0.2.4's `update`)

## [0.2.4] - 2026-08-02

### Added

- `update` and `cleanup` subcommands for one-command lifecycle management, mirroring
  apple/container's `update-container.sh` / `uninstall-container.sh`. `update` downloads the
  latest release (or `--version vX.Y.Z`), verifies its checksum, replaces the binary in place,
  and re-runs `setup`; `--all` also updates OpenShell (brew) and apple/container, `--no-setup`
  replaces the binary only. `cleanup` layers like the apple/container uninstaller: the bare
  command removes only the driver's service and gateway wiring, `-d`/`--delete-data` also removes
  the driver's state, vmnet network and pulled images (`-k`/`--keep-data` is the default), and
  `--all` also removes OpenShell and apple/container. `uninstall` is now an alias for `cleanup`.

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

[Unreleased]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.12...HEAD
[0.2.12]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.11...v0.2.12
[0.2.11]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.10...v0.2.11
[0.2.10]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.9...v0.2.10
[0.2.9]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.8...v0.2.9
[0.2.8]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.7...v0.2.8
[0.2.7]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.6...v0.2.7
[0.2.6]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.5...v0.2.6
[0.2.5]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.4...v0.2.5
[0.2.4]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.3...v0.2.4
[0.2.3]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/vyncint/openshell-driver-applecontainer/releases/tag/v0.1.0
