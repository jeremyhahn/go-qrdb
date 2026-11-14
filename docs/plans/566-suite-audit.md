# #566 suite audit (folded #1331)

Rules (ruling 273(12)): one path per behaviour at the uppermost layer that
reaches it; remove duplicates, tests of the standard library or a
dependency, tautologies; replace sleeps standing in for synchronisation.
No behaviour loses its only test; the coverage gate stays met.

## Package order (largest test time first)

| Order | CI leg | Package | Test files / lines |
|---|---|---|---|
| 1 | coverage tenant-lifecycle, tenant | pkg/tenant | 78 / 22k |
| 2 | coverage seal-barrier, seal | pkg/seal | 94 / 24k |
| 3 | coverage server-raft, server | pkg/server | 177 / 48k |
| 4 | test-tags | remaining packages by test time | |

## Removed tests

| Package | Removed test | Reason | Test that keeps the path |
|---|---|---|---|
