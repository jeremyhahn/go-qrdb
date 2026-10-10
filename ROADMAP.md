# go-qrdb roadmap

> **Status: REFERENCE** · Owner: Software Department · Last verified: 2026-10-08
> Per handbook `software/conventions/roadmap.md`: the Software Department refreshes it; the project agent proposes changes by PR.

## Mission

The portfolio's core database and clustering engine: embedded, standalone or Raft-clustered. Owns the data plane, cluster formation, barrier and seal, Shamir custody and the ceremony, identity and the single RBAC PDP, all through `sdk/go` (handbook `docs/00` L1, §R).

## Current milestone

`Public release` milestone (M12): 5 open, 16 closed. Release candidate projected **2026-10-22**, bound by the go-objstore and go-truststrap candidates.

## Now

- #618 — nightly FIPS 140-3 green again — open
- #20 — FIPS gate measured and placed — in progress
- #22 — RetrieveCredential return type and secret-bearing DTOs ruled and applied — in progress
- #118 — clusterenroll on truststrap p256proto (FIPS handshake) — open
- #145 — protected key material across the EncryptionBarrier — open
- #605 and trains #610–#617 — v0.46 lanes merged — in review
- #620 — latest enclave, quicraft v0.20.0, objstore, truststrap — in review

## Next

- #275 — lease keepalive tail under p90 target — in train #605
- #489 — test doubles resolve like the registry — open
- #79 — unseal accumulator holds shares in protected memory — in progress
- #35 — sound RANGE plans over numeric indexes — in progress
- #172 — repo-wide clock seam — open
- #548 — owner decisions the backlog waits on — open

## Later

- #306 — shared port-reservation package
- #350 — triage of 473 unreachable symbols
- #457 — engine range delete

## Dependencies

- Up: go-quicraft v0.18.0 (latest v0.20.0), go-enclave v0.7.0-alpha (v0.8.0-alpha), go-objstore v0.3.0-alpha (v0.4.0-alpha), go-truststrap v0.4.0-alpha (v0.6.0-alpha), go-codec v1.4.0. #620 closes the gap.
- Down: go-xkms (pins v0.28.1-alpha), go-signal (v0.7.0-alpha), go-quicbaas (v0.30.1-alpha), go-trusted-platform residue (v0.19.0-alpha).

## Non-goals

- Hardware and cloud crypto: go-xkms. Application layer and composition: go-quicbaas. Raft primitives: go-quicraft.
