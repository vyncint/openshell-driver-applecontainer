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
