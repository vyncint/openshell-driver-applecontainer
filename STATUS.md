# Project status

Single source of truth for milestone state. Updated at every milestone boundary.

Pinned upstream: OpenShell **v0.0.96** (`5541398ccbda05fd951e08e5741b9ca090717f3a`).
Host: Apple silicon, macOS 26.6, apple/container 1.2.0, Go 1.26.5.

| Milestone | State | Notes |
|---|---|---|
| Phase 0 — repo bootstrap | **done** | Private repo, commit-msg hook active before first commit, repo-local identity `Vyncint Ng <vyncint@users.noreply.github.com>`. |
| Gate 0 — prerequisites | **done** | G0.1 pass; G0.2 Landlock absent → best_effort (custom-kernel escape hatch documented); G0.3 pass (virtiofs ro, no noexec, exec bit preserved); G0.4 pass (base + supervisor images pull for arm64); G0.5 pass (guest→host vmnet TCP proven; gateway `bind_address`+`server_sans` confirmed in source); G0.6 pass. Transcripts in docs/gate0.md. |
| M1 — contract recon | **done** | docs/CONTRACT.md; protos vendored (Apache-2.0, NOTICE); Go stubs via pinned buf plugins. 8 RPCs — kill criterion not triggered. |
| M2 — hello driver | **done** | Unit tests green (`go test -race`), socket 0700/0600, live acceptance passed: gateway selected `applecontainer` (in_tree=false), CLI authenticated, `sandbox create` reached our CreateSandbox. Transcript in docs/acceptance.md. |
| M3 — first live sandbox | **done** | Live acceptance passed: create → phase Ready (supervisor mTLS dial-back from the VM), `sandbox exec` ran commands, forbidden egress blocked with HTTP 403 by the in-guest proxy, delete removed VM + record. Three real failures found and fixed on the way (image-ls schema, image USER vs boot shim, default OCI caps vs netns) — docs/acceptance.md. |
| M4 — full lifecycle | pending | |
| M5 — images & resources | pending | |
| M6 — hardening & docs | pending | |
| CI | pending | |
| Release v0.1.0 | pending | M2 acceptance passed live, so a release is warranted once CI is green. |

## Known quirks / risks

- Extension drivers must launch the gateway with `--enable-mtls-auth true`; the default only
  covers docker/podman/vm (docs/acceptance.md).
- Homebrew installer registers the CLI against `https://[::1]:17670`; the gateway binds IPv4
  — corrected registration to `https://127.0.0.1:17670` on this machine.
- Guest kernel lacks Landlock (Gate 0.2): filesystem policy runs best_effort; per-sandbox
  custom kernel via driver config is the escape hatch.
- The Homebrew `openshell` launchd service crash-loops on a Mac without docker/podman until a
  driver is configured; stopped during development (gateway launched manually).
