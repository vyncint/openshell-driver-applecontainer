# Gate 0 — environment prerequisites

Measured on the development machine before any driver code was written. All transcripts below
are from real runs on this host (trimmed only for length; progress spinner lines removed).

## Host

```
$ uname -m
arm64
$ sw_vers -productVersion
26.6
$ container --version
container CLI version 1.2.0 (build: release, commit: 6e65319)
$ container system status
status             running
apiserver.version  container-apiserver version 1.2.0 (build: release, commit: 6e65319)
$ go version
go version go1.26.5 darwin/arm64
$ openshell --version
openshell 0.0.96
```

Tooling installed for this project: buf 1.72.0, golangci-lint 2.12.2, goreleaser 2.17.1.

OpenShell source pinned for contract work: tag `v0.0.96`, commit
`5541398ccbda05fd951e08e5741b9ca090717f3a`.

### Homebrew gateway service note

Immediately after install, the Homebrew `openshell` service crash-loops on this machine:

```
$ tail /opt/homebrew/var/log/openshell/openshell-gateway.err.log
Error:   × configuration error: no compute driver configured and auto-detection found
  │ no suitable driver; set --drivers or OPENSHELL_DRIVERS to kubernetes,
  │ podman, docker, or vm
```

No Docker/Podman/Kubernetes is present and the managed `vm` driver is Linux-only, so
auto-detection finds nothing — which is precisely the gap this driver fills. The service was
stopped (`brew services stop openshell`); during development the gateway is launched manually
with `--drivers applecontainer --compute-driver-socket …`.

## G0.1 — guest kernel seccomp: PASS

```
$ container run --rm alpine:latest sh -c 'uname -r; zcat /proc/config.gz | grep -E "^CONFIG_SECCOMP(_FILTER)?="'
6.18.15
CONFIG_SECCOMP=y
CONFIG_SECCOMP_FILTER=y
```

Both required options are `=y`. Project proceeds.

## G0.2 — Landlock: NOT PRESENT (degraded mode)

```
$ container run --rm alpine:latest sh -c 'zcat /proc/config.gz | grep -iE "LANDLOCK|^CONFIG_SECURITYFS="; mount -t securityfs securityfs /sys/kernel/security 2>&1'
# CONFIG_SECURITY_LANDLOCK is not set
CONFIG_LSM="landlock,lockdown,yama,loadpin,safesetid,selinux,smack,tomoyo,apparmor,ipe,bpf"
mount: mounting securityfs on /sys/kernel/security failed: No such file or directory
```

The default apple/container guest kernel (kata-static, Linux 6.18.15) does **not** compile in
Landlock: `CONFIG_SECURITY_LANDLOCK is not set`. The `landlock` entry in `CONFIG_LSM` is only
the boot-order string; the LSM itself is absent, and securityfs is not even mountable.
Consequence: OpenShell filesystem policy degrades to `best_effort` on this kernel. A custom
kernel with Landlock enabled can be supplied per sandbox via the driver's `kernel` config
passthrough (`container run -k`); documented as the escape hatch, not the default.

## G0.3 — volume mounts, env injection, exec semantics: PASS

```
$ ls -l $PROBE_DIR
-rw-r--r--  1 vyncint  wheel   6 Aug  1 23:00 data.txt
-rwxr-xr-x  1 vyncint  wheel  29 Aug  1 23:00 probe.sh
$ container run --rm -v $PROBE_DIR:/probe:ro --env OSHL_PROBE=value42 alpine:latest sh -c \
    'grep " /probe " /proc/mounts; ls -l /probe; /probe/probe.sh; echo "env: $OSHL_PROBE"; touch /probe/write-test'
virtiofs /probe virtiofs ro,relatime 0 0
-rw-r--r--    1 root     root             6 Aug  1 16:00 data.txt
-rwxr-xr-x    1 root     root            29 Aug  1 16:00 probe.sh
SCRIPT_RAN_OK
env: value42
touch: /probe/write-test: Read-only file system
```

Findings:
- `-v host:guest:ro` arrives as a **virtiofs** mount with flags `ro,relatime` — no `noexec`,
  no `nosuid`, and the executable bit is preserved (the probe script executed directly from
  the read-only mount).
- `--env` injection works as expected; read-only enforcement works.
- The boot-shim design (copy supervisor to a writable path, `chmod +x`, `exec`) is therefore
  not strictly required by today's mount semantics, but is kept as insurance against future
  `noexec` semantics and because `container cp` is known to drop the executable bit.
- `container run` also supports `--mount type=…,source=…,target=…,readonly`, `--tmpfs`,
  `--label`, `--cidfile`, `-k/--kernel`, `--kernel-arg`, `--entrypoint`, `-c/--cpus`,
  `-m/--memory`, `--network`, `--publish-socket` (full flag inventory recorded during probing).

## G0.4 — default sandbox image has arm64: PENDING (blocked on contract recon)

The exact default sandbox image reference is being derived from OpenShell v0.0.96 source
during contract recon (docs/CONTRACT.md). Once identified, the image is pulled on this host
and its manifest inspected for a linux/arm64 entry; the real transcript replaces this section.
If the image lacks arm64, fallback is building one from openshell-community; if that proves
impractical, this gate is marked blocked in STATUS.md.

## G0.5 — guest → gateway reachability: PASS (raw path proven)

The vmnet network for this project was created (`container network create oshl`,
subnet 192.168.65.0/24, host-side gateway IP 192.168.65.1). A TCP listener was bound on the
host's vmnet gateway IP at the gateway port, and probed from inside a guest VM:

```
host$ python3 -c '…bind(("192.168.65.1", 17670)); listen…'
listening on 192.168.65.1:17670
guest$ nc -z -w 3 192.168.65.1 17670 && echo GUEST_PROBE_OK
CONNECTED from ('192.168.65.2', 43859)
GUEST_PROBE_OK
```

Guests can open TCP connections to the Mac's vmnet gateway address. Two delivery options for
the OpenShell gateway (which binds 127.0.0.1:17670 by default):
(a) configure the gateway to listen on an additional address, or
(b) run a local forwarder (`socat TCP-LISTEN:17670,bind=192.168.65.1,fork TCP:127.0.0.1:17670`).
The choice made for live acceptance is recorded in docs/CONTRACT.md once the gateway config
surface is confirmed from source.

Additional observation: guest eth0 MTU is 1280 on vmnet (visible both in-guest and in
`container ls` network options) — noted, no action needed.

## G0.6 — Unix socket under SUN_LEN: PASS

```
$ python3 -c 'socket(AF_UNIX).bind("/tmp/oshl-ac/driver.sock")'
bound ok: /tmp/oshl-ac/driver.sock 24 bytes
```

24 bytes, far below the macOS `sun_path` limit (~104).

## Defaults observed (for driver sizing decisions)

From `container ls --format json` on a probe container: default resources are
`cpus: 4, memoryInBytes: 1073741824` (1 GiB), per-VM IPv4 from the network subnet with
`ipv4Gateway` exposed per attachment. `status.state` value observed in this session:
`running`; the full state-value set is enumerated during M4 lifecycle work.

## Unverified assumptions at Gate 0 close

- G0.4 is not yet verified — it stays PENDING until the source-derived image reference is
  pulled and its manifest inspected on this host.
- Whether the gateway supports listening on a non-loopback address purely via configuration
  (option a) is resolved during M1/M2 from gateway source and docs; the raw network path is
  proven either way.
- `status.state` value `stopped` after `container stop` has been observed in prior lab work
  on this machine but not yet re-verified in this project; it is re-checked when lifecycle
  work lands in M4.
