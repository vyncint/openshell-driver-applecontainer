# openshell-driver-applecontainer

An out-of-tree extension compute driver for [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell)
backed by [apple/container](https://github.com/apple/container): every OpenShell sandbox is its
own micro-VM with a dedicated Linux kernel on Apple silicon.

<!-- TODO when public: CI badge, release badge
[![ci](https://github.com/vyncint/openshell-driver-applecontainer/actions/workflows/ci.yml/badge.svg)](…)
-->

## Why

OpenShell's managed VM driver gives every sandbox its own kernel, but its host networking layer
is Linux-only nftables, so macOS users are pointed at Docker Desktop — containers sharing one
Linux VM. apple/container natively provides the two expensive subsystems a VM-per-sandbox
driver otherwise has to build: OCI images become bootable EXT4 root disks, and every VM gets a
routable IP on a vmnet network the Mac can reach directly. This driver wires those primitives
to OpenShell's compute-driver contract, so a Mac gets true VM-per-sandbox isolation with the
stock OpenShell gateway, CLI, policy engine, and in-guest supervisor, unmodified. Measured on
an Apple-silicon dev machine: **mean 1.1 s from `sandbox create` to public phase `Ready`**
(VM boot + supervisor mTLS dial-back + gateway promotion; 10-cycle soak, e2e/soak.sh).

The driver is a lifecycle plane only. The in-guest `openshell-sandbox` supervisor enforces
policy (seccomp, network proxy, Landlock where available) and dials back to the gateway over
mTLS; exec and connect traffic ride that connection. The driver boots VMs correctly, delivers
supervisor + TLS material + environment, manages lifecycle state, and cleans up.

## Architecture

```
                     ┌──────────────────────────── Mac host ────────────────────────────┐
                     │                                                                   │
 openshell CLI ──mTLS──► openshell-gateway ──gRPC/unix socket──► openshell-driver-       │
                     │        ▲   (selects driver "applecontainer")   applecontainer     │
                     │        │                                        │ exec `container`│
                     │        │                                        ▼                 │
                     │        │                              apple/container apiserver   │
                     │        │                                        │                 │
                     │        │ supervisor dials back                  ▼ boots           │
                     │        │ (mTLS, vmnet)                ┌─── micro-VM (own kernel) ─┐
                     │        └──────────────────────────────│ openshell-sandbox         │
                     │                                       │  supervisor (root)        │
                     │   /openshell-seed (virtiofs, ro):     │   └─ workload (image user)│
                     │   supervisor bin, TLS, token, boot.sh └───────────────────────────┘
                     └───────────────────────────────────────────────────────────────────┘
```

Per sandbox, the driver: persists the accepted launch record, extracts the release-matched
supervisor binary (cached by image digest), builds a read-only seed directory (supervisor,
gateway CA + shared client cert/key, per-sandbox JWT, boot shim), then boots
`container run -d --uid 0 --cap-add …` with the seed mounted at `/openshell-seed` and the
boot shim as entrypoint. The shim copies the supervisor to a writable path, restores the
executable bit, and execs it. Progress and state flow to the gateway through the
`WatchSandboxes` stream, backed by a 2 s runtime poller; the runtime — not the driver's
records — is the source of truth.

## Quickstart

Prerequisites: Apple silicon, macOS 26+, [apple/container](https://github.com/apple/container)
1.2+, OpenShell 0.0.96 (see the compatibility table).

```sh
# 1. One-time host prep: dedicated vmnet network + images
make prep

# 2. Start the driver (standalone; the gateway never spawns it)
bin/openshell-driver-applecontainer \
  --grpc-endpoint https://192.168.65.1:17670   # gateway address AS SEEN FROM GUESTS

# 3. Make the gateway reachable from guest VMs (once):
#    listen beyond loopback and add a cert SAN for the vmnet gateway IP
openshell-gateway generate-certs --output-dir <your tls dir> --server-san 192.168.65.1
openshell-gateway --bind-address 0.0.0.0 --enable-mtls-auth true \
  --drivers applecontainer --compute-driver-socket /tmp/oshl-ac/driver.sock

# 4. Use OpenShell normally
openshell sandbox create --name demo
openshell sandbox exec -n demo -- uname -a
openshell sandbox delete demo
```

Instead of launch flags, the gateway TOML config can select the driver:

```toml
[openshell.gateway]
compute_drivers = ["applecontainer"]
bind_address = "0.0.0.0:17670"

[openshell.drivers.applecontainer]
socket_path = "/tmp/oshl-ac/driver.sock"
# NOTE: extension driver tables carry ONLY socket_path; all other driver
# settings are flags/env on the driver process itself.
```

> `--enable-mtls-auth true` is required: the gateway's local single-user mTLS auth defaults
> on only for the built-in docker/podman/vm drivers.

## Configuration reference

Flags on `openshell-driver-applecontainer` (env fallback `OSHL_AC_<NAME>`, flag wins):

| Flag | Default | Purpose |
|---|---|---|
| `--socket` | `/tmp/oshl-ac/driver.sock` | gRPC unix socket (keep short: macOS sun_path limit) |
| `--state-dir` | `~/.local/state/openshell-applecontainer` | sandbox records, seed dirs, supervisor cache |
| `--network` | `oshl` | vmnet network for sandbox VMs |
| `--default-image` | `ghcr.io/nvidia/openshell-community/sandboxes/base:latest` | advertised via GetCapabilities |
| `--supervisor-image` | `ghcr.io/nvidia/openshell/supervisor:0.0.96` | release-matched supervisor source |
| `--grpc-endpoint` | (required for create) | gateway endpoint reachable from inside guests |
| `--guest-tls-ca/cert/key` | gateway TLS state dir (`$OPENSHELL_LOCAL_TLS_DIR` honored) | client TLS triple handed to sandboxes |
| `--namespace` | `default` | namespace reported on sandboxes |
| `--cpus` / `--memory` | `2` / `2048` (MiB) | VM sizing when the request has no resources |
| `--kernel` | (runtime default kernel) | host kernel path used for **every** sandbox VM — the fleet-wide Landlock escape hatch; per-sandbox driver config overrides it |
| `--log-level` | `info` | driver log level and sandbox default |

## Per-sandbox driver config

`openshell sandbox create --driver-config-json '{"applecontainer": {…}}'`:

```json
{
  "mounts": [
    {"type": "volume", "source": "/Users/me/data", "target": "/data", "read_only": true},
    {"type": "tmpfs",  "target": "/scratch"}
  ],
  "network": "other-vmnet",
  "kernel":  "/path/to/custom/vmlinux"
}
```

- `volume` bind-mounts a host directory (apple/container has no named volumes; `source` is a
  host path). `read_only` defaults to **true**.
- Targets at or under `/opt/openshell`, `/etc/openshell`, `/etc/openshell-tls`, `/run/netns`,
  `/sandbox`, or `/openshell-seed` are rejected.
- `kernel` passes through to `container run --kernel` — e.g. a kernel built with Landlock
  enabled (see limitations).
- Unknown keys are rejected.

Resource requests (`openshell sandbox create --cpu 3 --memory 3Gi`) map to **real VM sizing**
(`--cpus` / `--memory`), unlike the upstream VM driver, which accepts but ignores them.
Limits win over requests; Kubernetes quantity strings are accepted; apple/container adds one
`cpuOverhead` vCPU on top of the request.

## Limitations

- **Landlock is absent from the default guest kernel** (kata-static 6.18.x:
  `CONFIG_SECURITY_LANDLOCK is not set`), so OpenShell filesystem policy degrades to
  `best_effort` (an alert is emitted in-guest; seccomp and the network policy proxy are
  unaffected). Escape hatch: a Landlock-enabled kernel build, either fleet-wide via the
  driver's `--kernel` flag or per sandbox via the driver-config `kernel` field.
- **No host-side nftables defense layer** — that upstream mechanism is Linux-only. On macOS,
  vmnet NAT already blocks inbound traffic from off the Mac; a pf anchor reproducing the
  "guests may only reach the gateway port" rule is possible future work.
- `StopSandbox` returns `Unimplemented` (the gateway never calls it in v0.0.96; the managed
  VM driver does the same).
- GPU sandboxes are rejected (`ValidateSandboxCreate` fails them explicitly).
- One `cpuOverhead` vCPU is added by apple/container on top of the requested count.

## Compatibility

| driver | OpenShell (pinned tag) | apple/container | host |
|---|---|---|---|
| v0.1.x | v0.0.96 (`5541398ccbda`) | 1.2.0 | Apple silicon, macOS 26 |

## Development

```sh
make test    # go test -race ./...
make lint    # golangci-lint run
make build   # bin/openshell-driver-applecontainer (version injected)
make proto   # regenerate stubs from vendored protos (buf)
make prep    # idempotent host prep: vmnet network + images
make e2e     # live smoke: create -> Ready -> exec -> policy block -> delete
make soak    # 10x lifecycle cycles with mean create->Ready latency
```

Unit tests run everywhere; `prep`/`e2e`/`soak` require this Mac-shaped environment (see
`.github/workflows/e2e.yml` — hosted CI never runs them). Real transcripts of every live
acceptance live in `docs/acceptance.md`; environment probes in `docs/gate0.md`; the contract
recon in `docs/CONTRACT.md`.

## Install (manual, pre-release)

Download the `darwin_arm64` archive from a release, unpack, and clear the quarantine bit
(unsigned binary):

```sh
xattr -d com.apple.quarantine openshell-driver-applecontainer
```

<!-- TODO when public: enable the Homebrew tap section in .goreleaser.yaml and document
     `brew install` here (a private tap is useless). -->

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE) (vendored proto attribution).
