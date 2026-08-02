# Contributing

Contributions are welcome. Maintainers (see [MAINTAINERS.md](MAINTAINERS.md)) review and
merge pull requests; the [Code of Conduct](CODE_OF_CONDUCT.md) applies in all project spaces.

## One-time repo setup

```sh
git config core.hooksPath .githooks
```

This enables two hooks: one appends the DCO `Signed-off-by` trailer to your commits
automatically, the other rejects forbidden attribution patterns (see Commits below).

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
coherent.

**DCO sign-off is required.** Every commit must carry a `Signed-off-by:` trailer matching
its author ([Developer Certificate of Origin](https://developercertificate.org/)) — use
`git commit -s`, or set `core.hooksPath .githooks` once and the trailer is added for you.
Commits made through the GitHub web UI are signed off automatically (a repository setting).
CI checks every pull request commit.

**Commits are single-author.** Commit messages must not contain `Co-authored-by` or
tool-attribution trailers of any kind — a commit-msg hook and the `commit-audit` CI job
reject them across the whole history. Credit collaborators by splitting work into commits
authored by each person instead.

## Compatibility discipline

The OpenShell contract is pinned (tag + commit in NOTICE and docs/CONTRACT.md). When bumping
the pin: re-run the contract recon against the new tag, update vendored protos + NOTICE, and
re-run the live acceptance before claiming support in the README compatibility table.
