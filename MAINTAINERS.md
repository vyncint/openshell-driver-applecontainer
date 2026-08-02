# Maintainers

Maintainers review and merge pull requests, triage issues, cut releases, and steward the
project's direction.

| Name | GitHub | Role |
|---|---|---|
| Vyncint Ng | [@vyncint](https://github.com/vyncint) | Lead maintainer |

## How merging works

- Every change lands through a pull request against `main` (the branch is protected: required
  CI checks, up-to-date branch, resolved conversations, DCO sign-off).
- A maintainer reviews and merges external contributions. Maintainer-authored PRs merge on
  green CI (with a sole maintainer there is no second reviewer; this table grows before that
  policy does).
- Releases are tagged by a maintainer from `main`; the release pipeline is automated
  (`.goreleaser.yaml`, `.github/workflows/release.yml`).

## Becoming a maintainer

Sustained, high-quality contributions (code, review, triage) over time, followed by a
nomination from an existing maintainer. Open an issue if you are interested in helping
maintain the project.
