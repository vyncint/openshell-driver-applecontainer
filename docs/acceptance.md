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
