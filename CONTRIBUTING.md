# Contributing

## Build and test

```sh
make build   # bin/openshell-driver-applecontainer
make test    # go test -race ./...
make lint    # golangci-lint run (same pinned version as CI)
make proto   # regenerate gRPC stubs from vendored protos (buf)
```

`go vet`, `golangci-lint run`, and `go test -race ./...` must pass before every push — CI
runs exactly these. Live end-to-end work additionally needs an Apple-silicon Mac with
apple/container and OpenShell installed (`make prep`, `make e2e`, `make soak`); see
docs/acceptance.md for what those flows are expected to prove.

## Ground rules

- Every `container …` invocation goes through the `backend.Runtime` interface so unit tests
  can use the fake. No direct exec calls elsewhere.
- Contexts everywhere; create/delete must stay cancel-safe (a delete arriving mid-create
  cancels provisioning and cleans up the orphan VM — there is a test for it).
- Table-driven tests; behavior that shells out gets golden tests over the constructed
  argument vectors; JSON parsing gets fixtures captured from the real CLI.
- Never claim "verified" or "tested" in docs or commit messages for anything that was not
  actually run; live transcripts go to docs/acceptance.md.

## Commits

Conventional Commits (`feat:`, `fix:`, `docs:`, `ci:`, `test:`, `chore:`), small and
coherent. The repo enforces a commit-msg hook (`git config core.hooksPath .githooks` is set
locally; CI mirrors it). DCO-style `Signed-off-by` is welcome but optional.

## Compatibility discipline

The OpenShell contract is pinned (tag + commit in NOTICE and docs/CONTRACT.md). When bumping
the pin: re-run the contract recon against the new tag, update vendored protos + NOTICE, and
re-run the live acceptance before claiming support in the README compatibility table.
