# Lane tsa1330t — #1330 pkg/api/grpc slice, verification on main d816c57b6b

No check failed, so no code changed and this lane made no commits.

| Check | rc |
|---|---|
| `make test-pkg PKG=./pkg/api/grpc/...` | 0 |
| `go test -tags "$(make -s print-build-tags)" -cover ./pkg/api/grpc/`: 93.6% vs baseline 94.03 − 1.0 = 93.03 | 0 (pass) |
| `make vet` | 0 |
| `make lint` (golangci-lint: 0 issues) | 0 |
| `gofmt -l pkg/api/grpc` is empty | pass |
| `git merge-base --is-ancestor d816c57b6b HEAD` | 0 |
| all 14 commits in d816c57b6b..HEAD have a gpgsig header | pass (only checked that the header exists: the lane has no allowedSignersFile, so `%G?` cannot verify the signature) |
| report.md untracked | yes |

## Coverage
- main d816c57b6b: 93.6%
- branch: 93.6%

## Wall time: `go test -tags "$(make -s print-build-tags)" -count=1 ./pkg/api/grpc/`
Both runs had a warm build cache. The branch ran first and main straight after.
- main (worktree at /tmp/m): 21 s wall, 10.0 s test time
- branch: 26 s wall, 15.0 s test time

The `-cover` runs had test times of 10.4 s on main and 10.7 s on the branch. So the 5 s gap in the plain run is probably noise on the host, not a regression in the slice.
