# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately via GitHub security advisories:
**Security → Report a vulnerability** on this repository. Do not open public issues for
security reports.

Reports should include the affected version/commit, reproduction steps, and impact. You can
expect an acknowledgement within a week.

## Scope notes

- The driver is a local, single-user component: its gRPC socket is owner-only (0600 in a
  0700 directory) and it executes the `container` CLI as the invoking user.
- Sandbox policy enforcement happens inside the guest (OpenShell supervisor); the driver's
  security responsibilities are correct boot material handling (TLS, per-sandbox JWT with
  0600 permissions, no secrets in env/logs/records) and mount-target validation.
- Known, documented degradation: the default guest kernel lacks Landlock, so OpenShell
  filesystem policy runs `best_effort` (docs/gate0.md G0.2, README limitations).
