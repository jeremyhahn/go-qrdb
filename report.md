# tsa1330r — #1330 pkg/api/quic slice, verification on main d816c57b6b

No check failed, so no code was edited.

| Check | rc |
|---|---|
| `make test-pkg PKG=./pkg/api/quic/...` | 0 |
| `make test-pkg PKG=./pkg/pin/...` | 0 |
| `go test -tags "$(make -s print-build-tags)" -cover ./pkg/api/quic/ ./pkg/pin/` | 0 |
| `make vet` | 0 |
| `make lint` (golangci-lint: 0 issues) | 0 |
| `gofmt -l pkg/api/quic pkg/pin` (empty) | 0 |
| `git merge-base --is-ancestor d816c57b6b HEAD` | 0 |
| gpgsig header on all 9 commits d816c57b6b..HEAD | yes (all 9) |
| report.md untracked | yes |

`git log %G?` shows `N` only because the lane has no `gpg.ssh.allowedSignersFile`.
The `gpgsig` header is present on every commit.

## Coverage (BUILD_TAGS)

| Package | baseline | floor (−1.0) | main d816c57b6b | branch |
|---|---|---|---|---|
| pkg/api/quic | 91.42 | 90.42 | 91.3% | 91.0% |
| pkg/pin | 97.22 | 96.22 | 97.2% | 97.5% |

## Wall time, `go test -tags "$(make -s print-build-tags)" -count=1 ./pkg/api/quic/`

Both runs used a warm build cache, one after the other:

- main (worktree /tmp/m at d816c57b6b): 16.5 s wall, package time 11.6 s
- branch: 17.4 s wall, package time 12.2 s

These numbers are within run-to-run noise. A cold first run on main took 40.1 s
wall (10.7 s package), and most of that was compiling.
