<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Multi-Database Architecture

> **Status: REFERENCE** · Last verified: 2026-10-13
> Index of the `databases` feature folder. The folder's five documents are the contract; this page holds no requirements of its own.

A QRDB **database** is a named, isolated data store owned by a tenant. A request
selects its database with the `X-QRDB-Database` header (REST), the
`x-qrdb-database` gRPC/Unix metadata key, the QUIC envelope `database` field, or
the MCP `database` tool argument. The **tenant** is never client-supplied; it
comes from the authenticated identity.

An earlier revision of this page described an engine type per database
(`kv`, `timeseries`, `vector`), a per-database `meta.json` directory layout, and
a one-time migration from a flat layout. The engine type was removed from
`database create`, so those sections were deleted rather than kept as a second,
disagreeing copy.

| Document | Holds |
|----------|-------|
| [architecture.md](architecture.md) | What the code does today ("Current state") and the target design |
| [engineering.md](engineering.md) | Implementation contract: backends, manager lifecycle, barrier wiring |
| [cli.md](cli.md) | The `qrdb database` command group |
| [api.md](api.md) | Operations and permissions on every transport |
| [user.md](user.md) | Task-oriented guide |

## Barrier encryption lifecycle

A database whose barrier is enabled is built by the node's
`database.DatabaseBarrierManager` (`pkg/database/barrier_manager.go`), is
named `database/<tenant>/<database>` in the cluster seal record, starts sealed, and
is unsealed and sealed through the database seal operations. Dropping the
database removes its key material. The self-managed and system-managed tenant
barrier modes are described in [../seal/multi-tenant.md](../seal/multi-tenant.md).

## Cross-References

- [Architecture Overview](../01-architecture.md)
- [Configuration Reference](../12-configuration.md)
- [API Reference](../api-server/api.md)
- [Tenant & Database Scoping](../scoping.md)
- [Multi-Tenancy: Isolation, Quotas & Entitlements](../multi-tenancy/architecture.md)
