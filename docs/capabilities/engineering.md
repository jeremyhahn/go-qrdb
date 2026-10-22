# Capabilities — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

## Exported Surface

`pkg/capabilities` exports `ModeStandalone`, `ModeRaft`, `RaftOnlyHint`,
`Capabilities`, `Register`, `Seal`, `Sealed`, `Get`, `Has`, and `Reset`.
`Reset` is for tests only.

`sdk/go` re-exports `Capabilities` as a type alias, and
`sdk/go/transport.AdminClient` exposes `Capabilities(ctx)`, which returns
`*transport.CapabilitiesResponse` (the same fields and JSON names).

`pkg/service` exports `WithCapabilityProfile`, the `AdminServiceOption` that
gives one admin service its own profile.

## Invariants

`Register` is idempotent. `Seal` stores a copy of `UnencryptedAtRest`, and `Get`
returns a copy so callers cannot mutate the sealed profile. The daemon seals
exactly once before accepting traffic. An SDK node does not seal; it passes its
profile through `WithCapabilityProfile`, and `AdminService.Capabilities`
returns that profile in preference to `Get`.

`Has` answers only whether a feature was registered by build-tagged code. It is
not runtime truth; use `Get` or `Admin().Capabilities` for the sealed profile.

## Errors

The capability read itself has no feature-specific typed error. Transport
failures map through each transport's normal admin error path. A feature that is
not available refuses at that feature's operation boundary, commonly with
`service.CapabilityUnsupportedError` for run-mode gates.

## Configuration

`config.Config.EffectiveCapabilities` is the config bridge. It applies the
current mode, optional build tags and config booleans such as `mvcc.enabled`,
`watch.enabled`, and `lease.enabled`. Cluster, backup, rebalance and etcd are
raft-only. On a raft node `statemachine.NarrowCapabilities` then clears what
the configured state machine does not serve.

## Testing

Run `make test-pkg PKG=./pkg/capabilities` for unit coverage and
`make integration-test-capabilities` for the feature target. The integration
coverage row is `test/integration/COVERAGE.md`'s
`[capabilities](../../docs/capabilities/)` row.
