# cx1317i report — #1317 (Closes #1200), Codex review 8941

## Done
- F2: a scheduled rotation commits only under its claim (fix(credentials) commit, with tests); report.md untracked.

## F1 in progress (wip commit — the tree does NOT build yet)
Design (settled by docs/multi-tenancy/architecture.md "tenant home store"): a tenant's
credential records live in the tenant's **home store** (clustered: the `__home` Raft
group, RF = cluster size, under the tenant's key; standalone: LocalHomes over the
tenant barrier). The node-local tenant barrier is only the migration legacy source.

Edited so far:
- pkg/credentials/service.go: ForBarrier replaced by `ForTenant(ctx, home Store, legacy *seal.StorageBarrier)`; migration keyed on legacy.
- pkg/credentials/store.go: `NewKVStore(kvstore.KVStore) (Store, error)` adapter (nil Get -> seal.ErrSecretNotFound; requires kvstore.AtomicKVStore).
- pkg/credentials/errors.go: ErrStoreNotAtomic; ErrNilStore/ErrNilBarrier docs.
- pkg/service/credential_service.go: `homes atomic.Pointer[homeSourceBox]`, `SetTenantHomes(tenant.HomeStoreSource)`; store() resolves HomeStore -> NewKVStore -> ForTenant; no source = CapabilityUnsupportedError.

## Remains
1. pkg/server/lifecycle/tenant_home.go attachHomeSource: also call `SetTenantHomes(source)` on n.Credential when it implements `interface{ SetTenantHomes(tenant.HomeStoreSource) }`.
2. pkg/secret/credential_resolver.go NewTenantScopedCredentialRetrieve still calls ForBarrier; it has NO production caller (tests only: credential_resolver_test.go, coverage_test.go, fixes_test.go) — delete it and its tenant tests (no-compat policy), or route via the service.
3. Tests: pkg/credentials/for_barrier_test.go -> ForTenant (nil home, nil legacy, shared audit, isolated from parent, legacy migrated into home); NewKVStore tests (nil, non-atomic, missing key); pkg/service/credential_tenant_test.go: set SetTenantHomes with a fake source over one shared kvstore memory store and show a policy set via service A is seen via service B (two services, two barriers, one home) — F1's multi-node test; plus no-source refusal.
4. Docs: docs/credentials/architecture.md (tenant store = home store), docs/credentials/engineering.md line ~152, docs/multi-tenancy/architecture.md table row ~935.
5. Acceptance: make test-pkg for ./pkg/credentials/..., ./pkg/api/mcp/..., ./pkg/service/..., ./cmd/qrdb/...; coverage; go vet per tag set (one per call); make lint; `git merge-base --is-ancestor d816c57b6b HEAD`.
