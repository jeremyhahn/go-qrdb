# #566 removal keepers at the tip (rw566w3)

One row per removed or trimmed test in [566-audit-2.md](566-audit-2.md)
to [566-audit-5.md](566-audit-5.md) and the removal tables of
[566-suite-audit.md](566-suite-audit.md) (the sleep sweeps replace waits
and remove nothing, so they have no rows here). Every line number is
1-based, as `git grep -n` prints it at the row tip: the keeper by
`git grep -n "func <Keeper>(" -- <file>`, the assertion by a grep for the
quoted call inside that function, a compile-time guard by its `var _` (or
`_ I = (*T)(nil)` block) line, a declared return type by its `func` line.
Where an audit record cited a line that is one off (it took a 0-based
editor line), the Verdict says so; the line here is the tip line.

| Removed test (file:func) | Record | Keeper at tip (file:line func) | Assertion or guard at tip (file:line, quoted call) | Verdict |
|---|---|---|---|---|
| cmd/qrdb/bindings_test.go:TestNotConnectedError_Interface | audit-3 | cmd/qrdb/bindings_test.go:1336 TestNotConnectedError_Message | :1339 `if err.Error() != expected` (exact message) | kept |
| cmd/qrdb/bindings_test.go:TestGUIUnavailableError_Interface | audit-3 | cmd/qrdb/bindings_test.go:1344 TestGUIUnavailableError_Message | :1347 `if err.Error() != expected` | kept |
| cmd/qrdb/bindings_test.go:TestAlreadyRunningError_Interface | audit-3 | cmd/qrdb/bindings_test.go:1391 TestAlreadyRunningError_Message | :1394 `if err.Error() != expected` | kept |
| cmd/qrdb/bindings_test.go:TestNotRunningError_Interface | audit-3 | cmd/qrdb/bindings_test.go:1399 TestNotRunningError_Message | :1402 `if err.Error() != expected` | kept |
| cmd/qrdb/host_adapter_test.go:TestHostAdapter_ImplementsServiceHost | audit-3 | guard | cmd/qrdb/host_adapter_test.go:36 `_ service.Host   = (*hostAdapter)(nil)` | kept |
| cmd/qrdb/host_adapter_test.go:TestBootstrapHostAdapter_ImplementsServiceHost | audit-3 | guard | cmd/qrdb/host_adapter_test.go:44 `_ service.Host   = (*bootstrapHostAdapter)(nil)` | kept |
| cmd/qrdbd/adapters_test.go:TestKVServiceAdapter_ImplementsKVStore | audit-3 | cmd/qrdbd/adapters_test.go:348 TestKVServiceAdapter_Put_Success | cmd/qrdbd/kvstore_adapter.go:55 `func newKVServiceAdapter(...) kvstore.AtomicKVStore`; adapters_test.go:362 `assert.Equal(t, "mykey", capturedKey)` | kept |
| cmd/qrdbd/start_coverage_test.go:TestNewKVServiceAdapter_ReturnsNonNil | audit-3 | cmd/qrdbd/adapters_test.go:348 TestKVServiceAdapter_Put_Success | adapters_test.go:362 `assert.Equal(t, "mykey", capturedKey)`, :363 `assert.Equal(t, []byte("myval"), capturedVal)` | kept |
| cmd/qrdbd/coverage_missing_test.go:TestWebUIHandler_ImplementsHTTPHandler | audit-3 | cmd/qrdbd/coverage_missing_test.go:76 TestWebUIHandler_ReturnsNil | :79 `assert.Nil(t, h, ...)`; return type cmd/qrdbd/webui.go:23 and webui_embed.go:32 `func webUIHandler() http.Handler` | kept |
| pkg/server/rest_registry_test.go:TestNewRESTServiceRegistry_ReturnsNonNil | audit-4 | pkg/server/rest_registry_test.go:68 TestRESTServiceRegistry_KnownDatabase_ReturnsService | :69 `r := NewRESTServiceRegistry(...)`, :75 `if svc == nil` on `r.KVService` | kept |
| pkg/database/factory_test.go:TestEmbeddedBackendFactory_InterfaceCompliance | audit-4 | guard | pkg/database/factory_test.go:115 `var _ BackendFactory = (*EmbeddedBackendFactory)(nil)` | kept (audit cited :114) |
| pkg/database/factory_test.go:TestRemoteBackend_InterfaceCompliance | audit-4 | guard | pkg/database/factory_test.go:263 `var _ Backend = (*RemoteBackend)(nil)` | kept (audit cited :262) |
| pkg/database/database_test.go:TestDatabase_Backend_NotNil | audit-4 | pkg/database/database_test.go:157 TestDatabase_Backend | :167 `if got != b` (identity of the stored backend) | kept |
| pkg/seal/splitter_test.go:TestShareSplitter_ImplementsInterface | audit-4 | guard | pkg/seal/shamir.go:165 `var _ ShareSplitter = (*shamirShareSplitter)(nil)`; :172 `func NewShamirShareSplitter() ShareSplitter` | kept (audit cited :164, :171) |
| pkg/sql/provider/index_adapter_test.go:TestQRDBIndexImplementsSQLIndex | audit-4 | pkg/sql/provider/index_adapter_test.go:206 TestNewQRDBIndex_IDFormat | index_adapter.go:60 `var _ sqle.Index = (*qrdbIndex)(nil)`; test :208 `assert.Equal(t, "mydb.users.Email", idx.id)` | kept (audit cited :59) |
| pkg/sql/provider/relational_ddl_test.go:TestDatabase_ImplementsDDLInterfaces | audit-4 | guard; pkg/sql/provider/readonly_database_test.go:126 TestReadOnlyDatabase_HidesRelationalDDLInterfaces | pkg/sql/provider/relational_ddl.go:32-37 `_ sqle.TableCreator = (*Database)(nil)` ... `_ sqle.SchemaValidator = (*Database)(nil)` | kept (audit cited :31-36) |
| pkg/sql/provider/relational_table_ddl_test.go:TestRelationalTable_ImplementsDDLInterfaces | audit-4 | guard | pkg/sql/provider/relational_table_ddl.go:31-37 `_ sqle.AlterableTable = (*RelationalTable)(nil)` ... `_ sqle.CheckAlterableTable = (*RelationalTable)(nil)` (seven guards) | kept (audit cited :30-34) |
| pkg/sql/provider/relational_table_test.go:TestRelationalTable_ImplementsInterfaces | audit-4 | guard | pkg/sql/provider/relational_table.go:42-46 `_ sqle.Table = (*RelationalTable)(nil)` ... `_ sqle.IndexedTable = (*RelationalTable)(nil)` | kept (audit cited :41-45) |
