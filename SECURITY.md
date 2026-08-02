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
