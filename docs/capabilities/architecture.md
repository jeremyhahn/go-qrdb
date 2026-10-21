# Capabilities — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
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
`config.Config.EffectiveCapabilities`, then seal it with `capabilities.Seal`
before serving traffic. After sealing, `Get` returns the sealed profile; before
sealing it returns a fallback built from the compile-time registry alone.

The public report flows through `service.AdminServicer.Capabilities`, then the
REST, gRPC, QUIC, MCP, Unix socket and embedded transports. The CLI reaches it
only through the SDK with `qrdb admin capabilities`.

## Data Flow

Build tags register optional features during `init`. The daemon or embedded
node loads configuration, applies run-mode gates and config gates, seals the
profile, and exposes the same struct on every transport. Seal-at-rest gaps are
reported in `unencryptedAtRest`; they are warnings, not startup refusals.

## Trust Boundaries

The capability report is an admin read guarded by `admin:capabilities` on
authenticated transports. It does not grant access to the reported feature; each
feature still authorizes its own operations.

## Out Of Scope

Capabilities are read-only at runtime. Changing configuration or build tags
requires a new process with a newly sealed profile.
