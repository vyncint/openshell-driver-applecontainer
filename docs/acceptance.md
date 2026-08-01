# Live acceptance transcripts

Real runs against the OpenShell 0.0.96 gateway on the development Mac. Trimmed for length
(ANSI codes and unrelated log lines removed); nothing is synthesized.

## M2 — gateway selects the driver; create reaches CreateSandbox

Setup (two processes, driver first — the gateway's socket dial is fail-fast):

```
$ ./bin/openshell-driver-applecontainer --log-level debug
time=… level=INFO msg="driver listening" socket=/tmp/oshl-ac/driver.sock version=30fdc5a
    network=oshl state_dir=…/.local/state/openshell-applecontainer
    default_image=ghcr.io/nvidia/openshell-community/sandboxes/base:latest

$ OPENSHELL_LOCAL_TLS_DIR="$HOME/.local/state/openshell/homebrew/tls" \
    openshell-gateway --bind-address 0.0.0.0 --enable-mtls-auth true \
    --drivers applecontainer --compute-driver-socket /tmp/oshl-ac/driver.sock
… INFO openshell_server: Using compute driver driver=applecontainer
… INFO openshell_server: Using remote compute driver endpoint driver=applecontainer socket=/tmp/oshl-ac/driver.sock
… INFO driver.initialize{driver.name=applecontainer}: openshell_server::compute:
    Compute driver connected configured_driver=applecontainer advertised_driver=applecontainer in_tree=false
… INFO openshell_server::gateway_listener: Server listening address=0.0.0.0:17670
```

`advertised_driver=applecontainer` is the value our `GetCapabilities` returned; `in_tree=false`
is the gateway classifying the driver as an out-of-tree extension.

CLI round trip (list served by our driver's empty `ListSandboxes`):

```
$ openshell status
  Gateway: openshell
  Server: https://127.0.0.1:17670
  Status: Connected
  Authentication: Authenticated (mTLS transport)
  Version: 0.0.96

$ openshell sandbox list
No sandboxes found.
```

Create hits our `CreateSandbox` (M2 behavior: structured log + `Unimplemented`):

```
$ openshell sandbox create -- bash
Error:   × code: 'Internal error', message: "create sandbox failed: CreateSandbox is
  │ not implemented yet"

driver log:
time=… level=INFO msg="create sandbox requested" sandbox_id=85d43295-7567-40d1-95d7-993da0f1bead
    name=succinct-turtle workspace=default
    image=ghcr.io/nvidia/openshell-community/sandboxes/base:latest

gateway log:
… grpc::sandbox: minted sandbox JWT sandbox_id=85d43295-7567-40d1-95d7-993da0f1bead
… rpc.method="CreateSandbox" … rpc.grpc.status_code=13 …
```

Note the image in the request: the CLI sent no image, the gateway defaulted it from **our**
capabilities response — confirming the M1 reading of the default-image flow. The gateway also
called `ValidateSandboxCreate` first (it passed) and rolled the store row back after the
Unimplemented create, so `sandbox list` stays empty.

## M3 — first live sandbox: create → Ready → exec → policy block → delete

Same gateway/driver setup as M2 (driver now with provisioning). Full cycle, all live:

```
$ openshell sandbox create --name m3-first
(driver log)
… msg="create sandbox requested" sandbox_id=dea33106-7a22-449d-a933-02389d2533ff name=m3-first
… msg=exec cmd="container run --detach --name oshl-dea33106-… --network oshl
    --volume …/sandboxes/dea33106-…/seed:/openshell-seed:ro
    --env HOME=/root --env OPENSHELL_ENDPOINT=https://192.168.65.1:17670
    --env OPENSHELL_SANDBOX=m3-first --env OPENSHELL_SANDBOX_COMMAND=sleep infinity
    --env OPENSHELL_SANDBOX_ID=dea33106-… --env OPENSHELL_SANDBOX_TOKEN_FILE=/openshell-seed/auth/sandbox.jwt
    --env OPENSHELL_SSH_SOCKET_PATH=/run/openshell/ssh.sock
    --env OPENSHELL_TLS_CA=/openshell-seed/tls/ca.crt … --env OPENSHELL_OCI_IMAGE_USER=sandbox
    --label openshell.ai/managed-by=openshell-driver-applecontainer …
    --cpus 2 --memory 2048M --uid 0 --gid 0
    --cap-add CAP_SYS_ADMIN --cap-add CAP_NET_ADMIN --cap-add CAP_SYS_PTRACE --cap-add CAP_SYSLOG
    --entrypoint /openshell-seed/boot.sh ghcr.io/nvidia/openshell-community/sandboxes/base:latest"

$ openshell sandbox list
NAME      CREATED              PHASE
m3-first  2026-08-01 16:54:14  Ready
```

`Ready` means the in-guest supervisor dialed the gateway over mTLS from the VM
(guest console shows the TLS 1.3 handshakes against 192.168.65.1 and a steady-state
session). Command execution and policy enforcement, via the OpenShell CLI:

```
$ openshell sandbox exec -n m3-first -- uname -a
Linux oshl-dea33106-7a22-449d-a933-02389d2533ff 6.18.15 #1 SMP … aarch64 GNU/Linux

$ openshell sandbox exec -n m3-first -- id
uid=998(sandbox) gid=998(sandbox) groups=998(sandbox)

$ openshell sandbox exec -n m3-first -- curl -sS -m 15 https://example.com
curl: (56) CONNECT tunnel failed, response 403        # policy-forbidden action BLOCKED
```

The workload runs as the image's non-root user (the supervisor consumed
`OPENSHELL_OCI_IMAGE_USER`), per-exec seccomp filters are visible in the console
(`Blocking socket domain via seccomp`), and the forbidden egress is denied by the
in-guest policy proxy with HTTP 403. Teardown:

```
$ openshell sandbox delete m3-first
✓ Deleted sandbox m3-first
$ container ls -a
ID  IMAGE  OS  ARCH  STATE  IP  CPUS  MEMORY  STARTED     # empty
$ openshell sandbox list
No sandboxes found.
```

### Failures hit on the way (real, and what fixed them)

1. `container image ls --format json` keeps the reference at `configuration.name`, not a
   top-level `reference` — the image-presence check missed a local image ("not present
   after pull"). Parser fixed against a captured fixture.
2. First boot died instantly: `mkdir /opt/openshell: Permission denied`. The base image sets
   `USER sandbox` and apple/container honors it for the entrypoint. Fix: run the boot shim
   as guest root (`--uid 0 --gid 0`), docker-driver parity.
3. Second boot: supervisor reached the gateway (provider env fetched over mTLS — proving the
   whole vmnet/TLS/token path) then died on
   `ip netns add … mount --make-shared /run/netns failed: Operation not permitted`.
   apple/container's guest init applies default OCI capabilities even for uid 0. Fix:
   `--cap-add CAP_SYS_ADMIN/CAP_NET_ADMIN/CAP_SYS_PTRACE/CAP_SYSLOG` (docker-driver parity).
   With the caps in place the netns, proxy, and policy layers all came up.

Note: an allowed-endpoint contrast probe (`curl https://api.anthropic.com/`) returned `000`
rather than an HTTP status; the default policy's exact allowlist lives in the
openshell-community image and is not verified in this tree, so no claim is made about it.

## M4 — lifecycle: restart adopt, exit detection, delete

apple/container VMs are owned by the system apiserver, so they keep running when the driver
dies — verified, and the basis for adopt-style reconcile (the upstream libkrun driver instead
re-launches, because its VMs die with it):

```
$ pkill -f openshell-driver-applecontainer ; container ls | grep -c oshl-
1                                            # VM alive with the driver gone
$ ./bin/openshell-driver-applecontainer …    # restart
(driver log)
… msg="reconciled sandbox record" sandbox_id=819b3176-… container=oshl-819b3176-…
    status=True reason=BackendReady          # adopted as Ready
$ openshell sandbox list
m4-adopt  2026-08-01 17:02:52  Ready
```

Out-of-band VM death is observed by the 2 s runtime poller and propagated through the watch
stream to the public phase:

```
$ container stop oshl-819b3176-…
(driver log)
… msg="sandbox state transition" sandbox_id=819b3176-… status=False reason=ContainerExited
$ openshell sandbox list
m4-adopt  2026-08-01 17:02:52  Error
```

Deleting the adopted (now stopped) sandbox removes VM, record, and state dir:

```
$ openshell sandbox delete m4-adopt
✓ Deleted sandbox m4-adopt
$ container ls -a            # empty
$ openshell sandbox list
No sandboxes found.
$ ls ~/.local/state/openshell-applecontainer/sandboxes/ | wc -l
0
```

Double-create (AlreadyExists) and delete-mid-create (provisioning cancellation with full
cleanup) are covered by unit tests against the fake runtime; orphaned-VM cleanup and
record-without-VM marking are covered by bootstrap reconcile tests.

## M5 — digest pinning, resource mapping, driver-config-json

```
$ openshell sandbox create --name m5-live --cpu 3 --memory 3Gi \
    --driver-config-json '{"applecontainer":{"mounts":[
      {"type":"tmpfs","target":"/scratch"},
      {"type":"volume","source":"/tmp/oshl-ac/m5data","target":"/data"}]}}' -- true

(driver log, boot argv — image pinned to the digest observed at inspect time)
… container run … --cpus 3 --memory 3072M --tmpfs /scratch
    --volume /tmp/oshl-ac/m5data:/data:ro …
    ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:aeef1c63f00e…

$ openshell sandbox exec -n m5-live -- sh -c '…'
cpus=4                                        # 3 requested + apple/container cpuOverhead: 1
tmpfs /scratch tmpfs rw,relatime 0 0
virtiofs /data virtiofs ro,relatime 0 0
m5-test-content                               # host file visible
touch: cannot touch '/data/w': Read-only file system
Mem:            3058 …                        # 3Gi applied

$ container ls --format json | …
oshl-17c1ccb2-… {'cpuOverhead': 1, 'cpus': 3, 'memoryInBytes': 3221225472}
```

Unlike the upstream VM driver (which accepts-but-ignores cpu/memory), requests map to real
VM sizing: `cpu_limit`/`cpu_request` → `--cpus` (Kubernetes quantities, rounded up),
`memory_limit`/`memory_request` → `--memory`. apple/container adds one `cpuOverhead` vCPU on
top of the request — visible as nproc = requested + 1. Volume mounts default to read-only;
reserved targets (`/opt/openshell`, `/etc/openshell`, `/etc/openshell-tls`, `/run/netns`,
`/sandbox`, `/openshell-seed`) are rejected at validate time.

## M6 — soak and shutdown

Ten full lifecycle cycles against the live gateway (e2e/soak.sh):

```
soak: cycle 1/10: create->Ready 1.1s
soak: cycle 2/10: create->Ready 1.1s
…
soak: cycle 10/10: create->Ready 1.1s
soak: PASS (10 cycles, mean create->Ready 1.1s, no VMs leaked)
```

The 1.1 s is measured from `sandbox create` to the **public** `Ready` phase — VM boot plus
supervisor mTLS dial-back plus gateway promotion — with a 0.5 s polling granularity. Each
cycle also ran an exec and a delete; `container ls -a` was verified empty at the end.

Graceful shutdown, observed at every driver restart during this work (SIGTERM → drain):

```
time=… level=INFO msg="shutting down, draining in-flight RPCs"
```

(When the long-lived gateway watch stream is open, the drain times out after 10 s and the
server force-stops — by design.)

### Environment quirks found (and their fixes)

1. The Homebrew installer registered the CLI gateway endpoint as `https://[::1]:17670`, but
   the gateway binds per `bind_address` (IPv4 by default) — the CLI could not connect at all.
   Fixed by pointing the registration at `https://127.0.0.1:17670`.
2. `--enable-mtls-auth` defaults **on only for docker/podman/vm gateways**. With an extension
   driver the default stays off and every CLI call fails with "missing authorization header".
   Local single-user gateways using this driver must pass `--enable-mtls-auth true`.
3. `openshell-gateway generate-certs` is incremental: re-running it with additional
   `--server-san` values refreshes the server certificate while preserving the CA, so
   existing client bundles stay valid. The server cert now carries SANs for
   `192.168.65.1`/`192.168.64.1` (vmnet gateway addresses) ahead of M3.
