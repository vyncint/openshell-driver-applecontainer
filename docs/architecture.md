# Architecture

## Components

```
openshell CLI ──mTLS──► openshell-gateway
                            │  RemoteComputeDriver (gRPC client)
                            ▼  unix socket /tmp/oshl-ac/driver.sock (0600, dir 0700)
              openshell-driver-applecontainer
                ├── grpcsvc   ComputeDriver service: validate/create/get/list/delete/watch
                │             registry (in-memory) + watch hub + 2 s runtime poller
                ├── state     <state-dir>/sandboxes/<id>/state.json launch records (0600)
                ├── seed      supervisor extraction cache + per-sandbox seed dirs
                └── backend   Runtime interface → exec `container …` (or a fake in tests)
                                   │
                                   ▼
                     apple/container apiserver ── micro-VM per sandbox (vmnet, own kernel)
```

Lifecycle only: exec/connect data-plane rides the supervisor's own dialed-back gateway
connection (RelayOpen/RelayStream on the same HTTP/2 session); the driver never touches it.

## Create sequence

```
gateway                     driver                         apple/container        guest
   │ ValidateSandboxCreate ──►│ id/gpu/driver-config/quantity checks
   │◄── ok ────────────────────│
   │ CreateSandbox(sandbox) ──►│ persist state.json (token redacted)
   │◄── {} (accepted) ─────────│ cond Ready=False/Starting; event Scheduled
   │                           │ ensure image local (else pull; events Pulling/Pulled)
   │                           │ pin digest; persist
   │                           │ extract supervisor (cache by image digest)
   │                           │ write seed dir: supervisor, TLS, jwt (0600), boot.sh
   │                           │ container delete -f oshl-<id>   (idempotency)
   │                           │ container run -d --uid 0 --cap-add … ─► boot VM (~1s)
   │◄─ watch: Ready=True/BackendReady + event Started              boot.sh: cp supervisor,
   │                           │                                   chmod +x, exec
   │◄──────────────────────────┼────────────────────────────────── supervisor dials back
   │ public phase → Ready (supervisor session connected)           (mTLS to vmnet gw IP)
```

Failure at any provisioning step → `Ready=False/ProvisioningFailed` (terminal) plus a
`Warning` platform event; the record survives for inspection until deleted.

## Reconciliation

- **Startup** (`Bootstrap`): records ↔ `container ls --all`. Running VM → adopt as Ready
  (apple/container VMs are owned by the system apiserver and survive driver restarts —
  verified live). Stopped VM → `ContainerExited`. No VM → `ProvisioningFailed`. Containers
  labeled `openshell.ai/managed-by=openshell-driver-applecontainer` without a record →
  deleted as orphans.
- **Runtime poller** (2 s, docker-driver cadence): flips conditions on state changes and
  publishes watch events. In-flight provisioning is skipped; original failure reasons are
  preserved. The gateway additionally reconciles via `ListSandboxes` every 60 s and prunes
  store rows absent from the backend after a 300 s grace period.

## Security posture

- Driver socket: directory 0700, socket 0600, live-instance detection, stale-socket recovery.
- Sandbox token: never in env or logs; 0600 file in the seed dir, redacted from persisted
  records (VMs are never re-launched from records, so the token need not be stored).
- Seed dir mounted read-only; user mounts cannot shadow reserved paths (including the seed).
- The boot shim + supervisor run as guest root with exactly the four capabilities the
  upstream docker driver grants (SYS_ADMIN, NET_ADMIN, SYS_PTRACE, SYSLOG) on top of the
  guest init's default set; workloads drop to the image's OCI user (verified live: uid 998).
