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

## Trust boundary: sandbox specs are trusted input

The driver treats the sandbox specification it receives from the gateway as trusted. In
particular, a per-sandbox `--driver-config-json` **volume** mount binds an arbitrary host
directory (its `source`) into the guest — read-only by default, writable on request — with
the invoking user's privileges. This is deliberate parity with the upstream docker/podman
drivers, and is safe in the intended single-user, single-tenant deployment where whoever
creates sandboxes already has that user's access.

Do **not** expose the gateway (and therefore this driver) to untrusted parties who can submit
sandbox specs without also trusting them with host filesystem access. Constraining volume
sources to an allowlist is tracked as a future enhancement; until then, the guest **target**
of a mount is validated (reserved paths rejected) but the host **source** is not.
