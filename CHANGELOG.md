# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow SemVer
(`v0.x.y` while pre-stable).

## [Unreleased]

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

[Unreleased]: https://github.com/vyncint/openshell-driver-applecontainer/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/vyncint/openshell-driver-applecontainer/releases/tag/v0.1.0
