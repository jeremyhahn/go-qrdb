# go-qrdb row table

One SPEC row = one row issue = one branch `row/<epic>-<slug>` = one lane
session = one PR (handbook `software/conventions/sdlc.md` §2b).
Every open issue is a row issue, sits under one, or is listed below as kept.
The row's PR closes its row issue and marks the row shipped in
[docs/SPEC.md](../docs/SPEC.md). Rows with no open work are not listed.

## SPEC rows

| Row | Row issue | Status | Folded | Open PRs |
|---|---|---|---|---|
| desktop-gui | #1478 | in progress | 0 | #1479, #1606, #1607, #1608, #1612 |
| separation-of-duties | #1129 | in progress | 0 | #1579 |
| strict-tenant-isolation | #1565 | in progress | 0 | #1576 |
| object-storage-credential-handle | #1566 | planned | 0 | #1577 |
| object-storage-provider | none | planned | 0 | none |
| object-storage-provider-seam | none | planned | 0 | none |
| arm64-release | #916 | in progress | 0 | none |
| verifiable-log | #1613 | in progress | 0 | none |
| release-readiness | #566 | planned | 1 | none |
| iam-exposure | #1651 | planned | 0 | none |
| rolling-upgrades | #1628 | planned | 1 | #1638 |
| multi-model-databases | #923 | future | 0 | none |
| object-storage-lsm | #924 | future | 0 | none |
| volume-pool-tenant-moves | #925 | future | 0 | none |
| sqlite-vfs | #926 | future | 0 | none |

## Seam rows (cross-repo, consumer go-quicbaas)

| Seam | Row issue | Open PRs |
|---|---|---|
| WebTransport on the HTTP/3 listener | #1244 | #1527, #1542 |
| credential rotation policy | #1201 | #1548, #1552, #1556, #1410 |
| tenant delegation for embedder work | #1187 | none (shipped by #1578) |
| QUIC public operations without a client certificate | #1174 | none (shipped by #1475) |

## Kept outside the rows

| Issue | Why |
|---|---|
| #1218 | Owner questions (cron, per-database objstore engine, watch prefix) |
| #1331 | Test-suite audit, ruling 273; not a SPEC row. Folded into release-readiness and merged (#1541, #1602) |

## Main-fix and CI PRs (not on a row)

| PR | What |
|---|---|
| #1444 | CI: Docker jobs on the bigdisk-docker pool (ruling 442) |
