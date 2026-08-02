# Architecture

`openshell-driver-applecontainer` is an out-of-tree [OpenShell](https://github.com/NVIDIA/OpenShell)
compute driver whose backend is [apple/container](https://github.com/apple/container). Every
sandbox is a micro-VM with its own Linux kernel on Apple silicon. The driver is a **lifecycle
plane only**: it boots the VM and delivers the supervisor, TLS material, and per-sandbox token;
policy enforcement and exec traffic live in the guest.

## Topology

```mermaid
flowchart TB
  cli["openshell CLI"]
  subgraph host["Mac host"]
    gw["openshell-gateway<br/>selects the applecontainer driver"]
    subgraph drv["openshell-driver-applecontainer"]
      grpc["grpcsvc — ComputeDriver RPCs<br/>registry · watch hub · 2s poller"]
      statepkg["state — launch records (0600)"]
      seedpkg["seed — supervisor cache · seed dirs"]
      backend["backend — exec container CLI"]
    end
    api["apple/container apiserver"]
    seed["/openshell-seed · virtiofs ro<br/>supervisor · TLS · JWT · boot.sh"]
    subgraph vm["micro-VM · own kernel · vmnet IP"]
      sup["openshell-sandbox supervisor · root"]
      wl["workload · image user"]
      sup --> wl
    end
  end
  cli -->|mTLS| gw
  gw -->|"gRPC · unix socket 0600 (dir 0700)"| grpc
  backend -->|"exec container run/ls/…"| api
  api -->|boots| vm
  seedpkg -.->|writes| seed
  seed -.->|"virtiofs ro"| vm
  sup -->|"dials back · mTLS / vmnet"| gw
```

The gateway's `RemoteComputeDriver` forwards every RPC over the Unix socket
(`/tmp/oshl-ac/driver.sock`). Exec and `connect` data ride the supervisor's own dialed-back
gateway connection (`RelayOpen`/`RelayStream` on the same HTTP/2 session); the driver never
touches that path.

## Create flow

Accept-then-provision: the driver persists the launch record and returns immediately, then
boots the VM in the background. The gateway promotes the sandbox to public `Ready` only once
the in-guest supervisor connects.

```mermaid
sequenceDiagram
  autonumber
  participant GW as gateway
  participant DRV as driver
  participant API as apple/container
  participant VM as micro-VM

  GW->>DRV: ValidateSandboxCreate
  DRV-->>GW: ok
  GW->>DRV: CreateSandbox
  Note over DRV: persist state.json (token redacted)
  DRV-->>GW: {} accepted — Ready=False / Starting
  Note over DRV: resolve + pin image (pull if absent)<br/>extract supervisor (cached by digest)<br/>write seed dir: supervisor, TLS, JWT (0600), boot.sh
  DRV->>API: container delete -f oshl-{id}  (idempotency)
  DRV->>API: container run -d --uid 0 --cap-add …
  API->>VM: boot (~1 s)
  Note over VM: boot.sh copies supervisor, chmod +x, exec
  DRV-->>GW: watch: Ready=True / BackendReady + Started
  VM->>GW: supervisor dials back (mTLS to vmnet gw IP)
  Note over GW: public phase → Ready<br/>(supervisor session connected)
```

Failure at any provisioning step yields `Ready=False / ProvisioningFailed` (terminal) plus a
`Warning` platform event carrying the guest console tail; the record survives for inspection
until deleted.

## Sandbox lifecycle

Driver-side conditions the gateway reads (`internal/grpcsvc/conditions.go`). A 2 s runtime
poller keeps them truthful against `apple/container`.

```mermaid
stateDiagram-v2
  [*] --> Provisioning: CreateSandbox accepted
  Provisioning --> Ready: VM running · BackendReady
  Provisioning --> Failed: boot error · ProvisioningFailed
  Ready --> Exited: VM stopped out-of-band · ContainerExited
  Ready --> Deleting: DeleteSandbox
  Exited --> Deleting: DeleteSandbox
  Failed --> Deleting: DeleteSandbox
  Deleting --> [*]: VM + record removed
```

## Reconciliation

The runtime — not the driver's records — is the source of truth.

```mermaid
flowchart TB
  start["startup: state.json records ↔ container ls -a"] --> q{"VM for this record?"}
  q -->|running| adopt["adopt → Ready"]
  q -->|stopped| exited["ContainerExited"]
  q -->|missing| failed["ProvisioningFailed"]
  orphan["managed-by VM · no record"] --> del["delete as orphan"]
```

- **Startup** (`Bootstrap`): apple/container VMs are owned by the system apiserver and survive
  a driver restart (verified live), so a running VM is **adopted**, not relaunched. Containers
  labeled `openshell.ai/managed-by=openshell-driver-applecontainer` without a record are
  deleted as orphans.
- **Runtime poller** (2 s, docker-driver cadence): flips conditions on state changes and
  publishes watch events. In-flight provisioning is skipped and original failure reasons are
  preserved. The gateway additionally reconciles via `ListSandboxes` every 60 s and prunes
  store rows absent from the backend after a 300 s grace period.

## Security posture

- **Driver socket**: directory 0700, socket 0600, live-instance detection, stale-socket
  recovery, and a symlink/ownership check on the (shared `/tmp`) socket directory.
- **Sandbox token**: never in env or logs; written 0600 in the seed dir and redacted from
  persisted records (VMs are never relaunched from records, so the token need not be stored).
- **Seed dir** mounted read-only; every seed file is owner-only. User-requested mounts cannot
  shadow reserved paths (including the seed), host **volume** mounts are opt-in
  (`--allow-host-mounts` / `--host-mount-root`), and the network override is allowlisted.
- **Privileges**: the boot shim and supervisor run as guest root with exactly the four
  capabilities the upstream docker driver grants (`SYS_ADMIN`, `NET_ADMIN`, `SYS_PTRACE`,
  `SYSLOG`) on top of the guest init's default set; workloads drop to the image's OCI user
  (verified live: uid 998).
