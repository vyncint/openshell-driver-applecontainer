# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow SemVer
(`v0.x.y` while pre-stable).

## [Unreleased]

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

[Unreleased]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/vyncint/openshell-driver-applecontainer/releases/tag/v0.1.0
