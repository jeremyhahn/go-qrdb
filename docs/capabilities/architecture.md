# Capabilities — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

## Problem

Clients need one runtime truth for what a node can serve. A false capability can
mean the feature was not compiled, is impossible in the current run mode, or was
disabled by configuration, so callers must not infer which gate closed from the
boolean alone.

## Shape

`pkg/capabilities` owns the `Capabilities` struct and the process-local registry.
Build-tagged files register optional compile-time features with `Register`.
Composition roots compute the runtime profile with
`config.Config.EffectiveCapabilities` and, on a raft node, narrow it to what the
configured state machine serves with `statemachine.NarrowCapabilities`. The
daemon (`cmd/qrdbd`) then seals it process-wide with `capabilities.Seal` before
serving traffic. An SDK node (`sdk/go/node`), of which one process can run
several, hands its profile to its own admin service with
`service.WithCapabilityProfile` instead, so each node reports its own. After
sealing, `Get` returns the sealed profile; before sealing it returns a fallback
built from the compile-time registry alone.

The public report flows through `service.AdminServicer.Capabilities` (the
per-node profile when one was given, else `Get`), then the
REST, gRPC, QUIC, MCP, Unix socket and embedded transports. The CLI reaches it
only through the SDK with `qrdb admin capabilities`.

## Data Flow

Build tags register optional features during `init`. The daemon or SDK node
loads configuration, applies run-mode, config and state-machine gates, fixes the
profile as above, and exposes the same struct on every transport. Seal-at-rest
gaps are reported in `unencryptedAtRest` and logged at startup by
`Capabilities.WarnUnencryptedAtRest`; they are warnings, not startup refusals.

## Trust Boundaries

The capability report is an admin read guarded by `admin:capabilities` on
authenticated transports. It does not grant access to the reported feature; each
feature still authorizes its own operations.

## Out Of Scope

Capabilities are read-only at runtime. Changing configuration or build tags
takes a restart, which computes a new profile.
