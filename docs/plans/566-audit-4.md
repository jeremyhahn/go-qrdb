# #566 suite audit 4: pkg/server, pkg/server/lifecycle, pkg/database, pkg/seal, pkg/sql/provider (rw566u)

Rules as in [566-suite-audit.md](566-suite-audit.md) (ruling 273(12)) and
[566-audit-2.md](566-audit-2.md): a non-nil or implements-interface test
goes when it asserts nothing a named test or a compile-time guard (a
`var _ Iface = ...` line, or a function whose declared return type is the
interface) does not already check, and is trimmed to a package-level
`var _` guard when none exists. A test named NonNil that asserts values or
identity is behaviour and stays. No production code changes; no tests
added.

| Removed or trimmed test (file:func) | Behaviour it checked; why removed | Still checked by (file:func or file:line guard) |
|---|---|---|
| pkg/server/rest_registry_test.go:TestNewRESTServiceRegistry_ReturnsNonNil | constructor non-nil; tautology | pkg/server/rest_registry_test.go:TestRESTServiceRegistry_KnownDatabase_ReturnsService |
| pkg/database/factory_test.go:TestEmbeddedBackendFactory_InterfaceCompliance | body was only the interface conversion; no production guard, so trimmed to one | pkg/database/factory_test.go:114 `var _ BackendFactory = (*EmbeddedBackendFactory)(nil)` |
| pkg/database/factory_test.go:TestRemoteBackend_InterfaceCompliance | same | pkg/database/factory_test.go:262 `var _ Backend = (*RemoteBackend)(nil)` |
| pkg/database/database_test.go:TestDatabase_Backend_NotNil | `Backend()` non-nil; the identity test is stronger | pkg/database/database_test.go:TestDatabase_Backend (asserts the stored backend is the one passed); pkg/database/encryption_atrest_test.go uses `db.Backend()` |
| pkg/seal/splitter_test.go:TestShareSplitter_ImplementsInterface | runtime type assertion of a compile fact | pkg/seal/shamir.go:164 `var _ ShareSplitter = (*shamirShareSplitter)(nil)`; shamir.go:171 `func NewShamirShareSplitter() ShareSplitter` |
| pkg/sql/provider/index_adapter_test.go:TestQRDBIndexImplementsSQLIndex | repeated the production guard plus a non-nil check | pkg/sql/provider/index_adapter.go:59 `var _ sqle.Index = (*qrdbIndex)(nil)`; index_adapter_test.go:TestNewQRDBIndex_IDFormat |
| pkg/sql/provider/relational_ddl_test.go:TestDatabase_ImplementsDDLInterfaces | type assertions of six DDL interfaces on `*Database` | pkg/sql/provider/relational_ddl.go:31-36 `var _` guards; readonly_database_test.go:132-137 (the read-only guard's positive control) |
| pkg/sql/provider/relational_table_ddl_test.go:TestRelationalTable_ImplementsDDLInterfaces | type assertions of five DDL interfaces on `*RelationalTable` | pkg/sql/provider/relational_table_ddl.go:30-34 `var _` guards |
| pkg/sql/provider/relational_table_test.go:TestRelationalTable_ImplementsInterfaces | function-local copy of the production guards | pkg/sql/provider/relational_table.go:41-45 `var _` guards |

| Kept candidate | Why it stays |
|---|---|
| pkg/server/server_test.go:TestWithTenantOnboarding_NonNil | asserts the stored value is the one passed (identity); no pkg/server test reaches the served path |
| pkg/server/start_options_test.go:TestWithClusterKeyReceiver_NonNil, TestWithBackupService_NonNil, TestWithRebalanceService_NonNil | same (identity of the stored option) |
| pkg/server/lifecycle/lifecycle_test.go:TestBuildServices_CoreServicesNonNil, TestBuildServices_SealGatedServicesNonNil | the only check that BuildServices wires each service (behaviour) |
| pkg/server/lifecycle/kv_adapter_guarded_batch_test.go NonNilEmptyValue tests | behaviour (guard semantics of an empty non-nil value) |
| pkg/server/tenant_infra_test.go:TestBuildTenantInfra_HostWithoutManager_AllOtherFieldsNonNil_ReturnsConfigError | behaviour (config error and cleanup) |
| pkg/database:TestAllErrorTypes_ImplementErrorInterface | asserts a non-empty Error() for 21 types (behaviour) |
| pkg/sql/provider/entity_table_test.go:TestCoerceReflectToSQL_NonNilPtr | asserts the coerced value (behaviour) |
