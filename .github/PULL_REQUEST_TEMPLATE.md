## What

<!-- What does this change do, and why? Link the issue it fixes: "Fixes #NN". -->

## Verification

<!-- What did you actually run? Unit tests always; for behavior changes on the
     live stack, paste the relevant transcript (see docs/acceptance.md style).
     Never claim something was tested that was not run. -->

## Checklist

- [ ] `go test -race ./...` and `golangci-lint run` pass locally
- [ ] Every commit is signed off (`git commit -s`, DCO — see CONTRIBUTING.md)
- [ ] Commit messages are single-author Conventional Commits — no
      `Co-authored-by` or tooling attribution trailers (CI enforces this)
- [ ] Docs updated if behavior or flags changed (README, CHANGELOG "Unreleased")
