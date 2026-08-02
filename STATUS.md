# Project status

Single source of truth for milestone state. Updated at every milestone boundary.

Pinned upstream: OpenShell **v0.0.96** (`5541398ccbda05fd951e08e5741b9ca090717f3a`).
Host: Apple silicon, macOS 26.6, apple/container 1.2.0, Go 1.26.5.

| Milestone | State | Notes |
|---|---|---|
| Phase 0 — repo bootstrap | **done** | Repo bootstrapped private; commit-msg hook active before first commit; repo-local identity `Vyncint Ng <vyncint@users.noreply.github.com>`. |
| Gate 0 — prerequisites | **done** | G0.1 pass; G0.2 Landlock absent → best_effort (custom-kernel escape hatch documented); G0.3 pass (virtiofs ro, no noexec, exec bit preserved); G0.4 pass (base + supervisor images pull for arm64); G0.5 pass (guest→host vmnet TCP proven; gateway `bind_address`+`server_sans` confirmed in source); G0.6 pass. Transcripts in docs/gate0.md. |
| M1 — contract recon | **done** | docs/CONTRACT.md; protos vendored (Apache-2.0, NOTICE); Go stubs via pinned buf plugins. 8 RPCs — kill criterion not triggered. |
| M2 — hello driver | **done** | Unit tests green (`go test -race`), socket 0700/0600, live acceptance passed: gateway selected `applecontainer` (in_tree=false), CLI authenticated, `sandbox create` reached our CreateSandbox. Transcript in docs/acceptance.md. |
| M3 — first live sandbox | **done** | Live acceptance passed: create → phase Ready (supervisor mTLS dial-back from the VM), `sandbox exec` ran commands, forbidden egress blocked with HTTP 403 by the in-guest proxy, delete removed VM + record. Three real failures found and fixed on the way (image-ls schema, image USER vs boot shim, default OCI caps vs netns) — docs/acceptance.md. |
| M4 — full lifecycle | **done** | Startup reconcile (adopt running / mark exited / fail missing / delete labeled orphans), 2 s runtime poller publishing transitions, delete with in-flight-create cancellation. Live: restart-adopt, out-of-band stop → Error phase, clean delete of adopted sandbox. |
| M5 — images & resources | **done** | Local-first resolution, digest pinned at inspect time and booted by digest, cpu/memory quantity mapping to real VM sizing (live-verified in-guest: 3Gi + cpus, with apple/container's +1 cpuOverhead noted), driver-config-json schema (volume/tmpfs mounts with reserved-target enforcement, network override, kernel passthrough). |
| M6 — hardening & docs | **done** | Socket perms enforced + tested, graceful SIGTERM drain, 10-cycle soak: mean create→Ready **1.1 s**, zero leaked VMs. README (architecture, quickstart, config + driver-config reference, limitations, compatibility), docs/architecture.md, CONTRIBUTING, SECURITY, CHANGELOG. |
| CI | **done** | First run green across all six jobs (lint, test ubuntu+macos, cross-build, commit-audit, govulncheck). e2e.yml is workflow_dispatch + self-hosted macOS only. |
| Release v0.1.0 | **done** | Tag `v0.1.0` pushed; release workflow green; assets published (`openshell-driver-applecontainer_0.1.0_darwin_arm64.tar.gz`, `checksums.txt`). |
| Risk-fix round | **done** | Issues #1–#3 fixed and merged (console-tail diagnostics, startup preflight, driver-level kernel). |
| One-command setup | **done** | `setup`/`uninstall` subcommands: launchd driver service, stock Homebrew gateway service wired via gateway.env, cert SAN, endpoint auto-derivation, image pre-pull. Live-verified including uninstall→setup round trip and KeepAlive restart. README rewritten quickstart-first. |
| Release v0.2.0 | **done** | Tag `v0.2.0` (one-command setup + zero-config defaults + risk-fix round); release workflow green; assets published. |
| Open-source prep | **in progress** | Governance set (MAINTAINERS, CODEOWNERS, code of conduct, templates), required DCO sign-off (hook + CI + web-commit setting), audit-driven doc fixes and code hardening. |

## Known quirks / risks

- Extension drivers must launch the gateway with `--enable-mtls-auth true`; the default only
  covers docker/podman/vm (docs/acceptance.md).
- Homebrew installer registers the CLI against `https://[::1]:17670`; the gateway binds IPv4
  — corrected registration to `https://127.0.0.1:17670` on this machine.
- Guest kernel lacks Landlock (Gate 0.2): filesystem policy runs best_effort; per-sandbox
  custom kernel via driver config is the escape hatch.
- The Homebrew `openshell` launchd service crash-loops on a Mac without docker/podman until a
  driver is configured; stopped during development (gateway launched manually).
