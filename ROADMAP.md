# go-qrdb roadmap

> **Status: REFERENCE** · Owner: Software Department · Last verified: 2026-10-11
> Per handbook `software/conventions/roadmap.md`: the Software Department refreshes it; the project agent proposes changes by PR.

## Mission

The portfolio's core database and clustering engine: embedded, standalone or Raft-clustered. Owns the data plane, cluster formation, barrier and seal, Shamir custody and the ceremony, identity and the single RBAC PDP, all through `sdk/go` (handbook `docs/00` L1, §R).

## Current milestone

- [Release candidate](../../milestones) — target: RC — projected: not yet set in the department plan
- [Public release](../../milestone/12) — target: release — projected: not yet set in the department plan

## Now

- [#618](../../issues/618), [#608](../../issues/608) — nightly FIPS 140-3 job green — `docs/fips/` (to add) — open
- [#20](../../issues/20) — FIPS gate measured and placed in CI — `docs/fips/` (to add) — open
- [#565](../../issues/565) — release-readiness checklist complete — [docs/release/](docs/release/) — open
- [#22](../../issues/22) — RetrieveCredential return type and secret-bearing DTOs applied — [docs/credentials/](docs/credentials/) — open
- [#145](../../issues/145) — protected key material across the EncryptionBarrier — [docs/barrier-seal/](docs/barrier-seal/) — open
- [#655](../../issues/655) — conditional factor add — [docs/auth-factors/](docs/auth-factors/) — open
- [#631](../../issues/631) — barrier passphrase change — [docs/barrier-seal/](docs/barrier-seal/) — open

## Next

- [#630](../../issues/630) — file backend encrypted at rest — [docs/storage-engines/](docs/storage-engines/) — open
- [#632](../../issues/632) — file backend guarded writes are atomic — [docs/guarded-swap/](docs/guarded-swap/) — open
- [#649](../../issues/649) — Storage CompareAndSwap on every backend — [docs/guarded-swap/](docs/guarded-swap/) — open
- [#650](../../issues/650) — scoped secret borrow and constant-time Equal — [docs/barrier-seal/](docs/barrier-seal/) — open
- [#633](../../issues/633) — secret-bytes entity field in protected memory — [docs/secondary-indexes/](docs/secondary-indexes/) — open
- [#646](../../issues/646) — DPoP proof carried in the request context — [docs/api-server/](docs/api-server/) — open
- [#79](../../issues/79) — unseal accumulator holds shares in protected memory — [docs/shamir-custody/](docs/shamir-custody/) — open

## Later

- [#118](../../issues/118) — clusterenroll on the truststrap P-256 handshake — [docs/clustering/](docs/clustering/) — open
- [#652](../../issues/652) — clusterenroll error paths covered — [docs/clustering/](docs/clustering/) — open
- [#35](../../issues/35) — sound RANGE plans over numeric indexes — [docs/secondary-indexes/](docs/secondary-indexes/) — open
- [#275](../../issues/275) — lease keepalive tail under the p90 target — [docs/leases/](docs/leases/) — open
- [#457](../../issues/457) — engine range delete — [docs/key-value/](docs/key-value/) — open
- [#627](../../issues/627) — lean standalone node build — [docs/run-modes/](docs/run-modes/) — open
- [#626](../../issues/626) — embedder extension seams — [docs/run-modes/](docs/run-modes/) — open
- [#636](../../issues/636) — actionlint in the lint gate — [docs/release/](docs/release/) — open
- [#350](../../issues/350) — unreachable symbols triaged — [docs/release/](docs/release/) — open
- [#306](../../issues/306) — shared port-reservation package — `docs/test-harness/` (to add) — open
- [#635](../../issues/635) — banner and stamp on every document — every `docs/<feature>/` — open
- [#658](../../issues/658) — feature specs scrubbed — every `docs/<feature>/` — blocked: see the issue

## Dependencies

- Up: go-quicraft v0.20.0, needs the rc.1 release ([#659](../../issues/659)).
- Up: go-enclave v0.8.0-alpha, go-objstore v0.4.0-alpha, go-truststrap v0.6.0-alpha, go-codec v1.4.0.
- Down: go-xkms, go-signal, go-quicbaas, go-trusted-platform residue.

## Non-goals

- Hardware and cloud crypto: go-xkms. Application layer and composition: go-quicbaas. Raft primitives: go-quicraft.
