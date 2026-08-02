# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately via GitHub security advisories:
**Security → Report a vulnerability** on this repository. Do not open public issues for
security reports.

Reports should include the affected version/commit, reproduction steps, and impact. You can
expect an acknowledgement within a week.

## Automated security tooling

Every push and pull request runs (`.github/workflows/ci.yml`):

- **govulncheck** — known-vulnerability scan of the dependency tree and Go stdlib, limited to
  symbols reachable from our code.
- **gosec** — static analysis. All crypto/TLS/injection/permission rules are active. Four rule
  classes are excluded as reviewed false-positives for a CLI-wrapping daemon (documented inline
  in the workflow): `G204` (subprocess — the driver execs the `container` CLI with constant
  commands and argv arrays, never a shell), `G304`/`G703` (file-by-path / taint — operator
  config paths, and sandbox-derived path components validated by `state.ValidID`), and `G101`
  (noisy on env-var-*name* constants; secret detection is owned by gitleaks / secret scanning).
- **gitleaks** — secret scan over the full commit history.

Repository configuration:

- **Dependabot** — alerts, security updates, and weekly version updates for Go modules and
  GitHub Actions (`.github/dependabot.yml`); the dependency graph is enabled.
- **CodeQL** (`.github/workflows/codeql.yml`) — dormant while the repo is private (it needs
  GitHub Advanced Security), activates automatically when the repo is public.
- **GitHub secret scanning + push protection** — activate when the repo is public; until then
  the gitleaks CI job covers secret detection.
- GitHub Actions are pinned to commit SHAs and kept current by Dependabot; workflow tokens use
  least-privilege permissions.

Run the same scans locally with `make sec` (see CONTRIBUTING.md).

## Scope notes

- The driver is a local, single-user component: its gRPC socket is owner-only (0600 in a
  0700 directory) and it executes the `container` CLI as the invoking user.
- Sandbox policy enforcement happens inside the guest (OpenShell supervisor); the driver's
  security responsibilities are correct boot material handling (TLS, per-sandbox JWT with
  0600 permissions, no secrets in env/logs/records) and mount-target validation.
- Known, documented degradation: the default guest kernel lacks Landlock, so OpenShell
  filesystem policy runs `best_effort` (docs/gate0.md G0.2, README limitations).

## Trust boundary: sandbox specs

The driver treats the sandbox specification it receives from the gateway as trusted, but the
two host-facing knobs a spec can set are gated by operator policy:

- **Host volume mounts are off by default.** A per-sandbox `--driver-config-json` `volume`
  mount binds a host directory into the guest, so it is rejected unless the driver is started
  with `--allow-host-mounts`, and `--host-mount-root` further constrains permitted `source`
  directories. `tmpfs` mounts never touch the host and are always allowed. The guest mount
  **target** is validated regardless (reserved paths rejected).
- **The per-sandbox network override is allowlisted.** A spec's `network` must be `--network`
  or one of `--allowed-networks`; it cannot attach a sandbox to an arbitrary vmnet network.

Even with these gates, treat the ability to submit sandbox specs as privileged in the
intended single-user, single-tenant deployment. Enabling `--allow-host-mounts` without a
`--host-mount-root` grants spec authors read/write to any of the invoking user's files.

## Network policy overlay (`--network-policy-file`)

Each sandbox image ships a default network policy (`/etc/openshell/policy.yaml`) that the
in-guest supervisor enforces — a per-(binary, destination) allowlist, not a simple domain
block. `--network-policy-file` lets the operator replace that file for every sandbox this
driver instance boots, by installing it as root during boot, before the supervisor starts
(the only point at which the file is writable — the shipped policy marks `/etc` read-only for
the running workload).

This control is **driver-wide and operator-only**: it is not exposed through
`--driver-config-json`, and there is no per-sandbox equivalent, because it changes the guest's
own security policy, not just this driver's behavior. Whoever can set the driver's flags
controls what every sandbox on this host can reach — same trust boundary as
`--allow-host-mounts`. A permissive overlay weakens every sandbox uniformly; scope it to the
specific hosts you need (start from the image's own policy and add entries — see the README's
[Network policy overrides](README.md#network-policy-overrides)), not to a blanket allow-all.
