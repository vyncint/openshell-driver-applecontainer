# openshell-driver-applecontainer

An out-of-tree extension compute driver for [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell)
backed by [apple/container](https://github.com/apple/container): every OpenShell sandbox runs as
its own micro-VM with a dedicated Linux kernel on Apple silicon.

> Status: under active development. See [STATUS.md](STATUS.md) for the current milestone state.

## Why

OpenShell's managed VM driver relies on a Linux-only host networking layer (nftables). On macOS,
apple/container natively provides the two expensive subsystems a VM-per-sandbox driver needs —
OCI image to EXT4 root disk conversion, and a routable vmnet network with per-VM IPs — so macOS
can get true VM-per-sandbox isolation without a Linux host.

## Layout

- `cmd/openshell-driver-applecontainer` — driver entrypoint (gRPC over a Unix socket)
- `internal/grpcsvc` — OpenShell compute driver service implementation
- `internal/backend` — runtime abstraction over the `container` CLI
- `internal/seed` — supervisor extraction and per-sandbox seed material
- `internal/state` — persisted sandbox records and restart reconciliation
- `docs/` — architecture, contract notes, and gate transcripts

## License

Apache-2.0. See [LICENSE](LICENSE).
