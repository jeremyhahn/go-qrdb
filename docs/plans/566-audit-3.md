# #566 suite audit 3: cmd/qrdb and cmd/qrdbd (rw566u)

Rules as in [566-suite-audit.md](566-suite-audit.md) (ruling 273(12)) and
[566-audit-2.md](566-audit-2.md): a non-nil or implements-interface test
goes when it asserts nothing a named test or a compile-time guard (a
`var _ Iface = ...` line, or a function whose declared return type is the
interface) does not already check; an error type's "satisfies error" test
goes when a message test of the same type exists (as
TestSQLExecutionError_ImplementsError did, 566-suite-audit.md:332). A test
named NonNil that asserts values or identity is behaviour and stays. No
production code changes; no tests added.

| Removed or trimmed test (file:func) | Behaviour it checked; why removed | Still checked by (file:func or file:line guard) |
|---|---|---|
| cmd/qrdb/bindings_test.go:TestNotConnectedError_Interface | `errors.As` on the bare `*NotConnectedError`; satisfying `error` is a compile fact | cmd/qrdb/bindings_test.go:TestNotConnectedError_Message |
| cmd/qrdb/bindings_test.go:TestGUIUnavailableError_Interface | same, `*GUIUnavailableError` | cmd/qrdb/bindings_test.go:TestGUIUnavailableError_Message |
| cmd/qrdb/bindings_test.go:TestAlreadyRunningError_Interface | same, `*AlreadyRunningError` | cmd/qrdb/bindings_test.go:TestAlreadyRunningError_Message |
| cmd/qrdb/bindings_test.go:TestNotRunningError_Interface | same, `*NotRunningError` | cmd/qrdb/bindings_test.go:TestNotRunningError_Message |
| cmd/qrdb/host_adapter_test.go:TestHostAdapter_ImplementsServiceHost | body was only the interface assignment; guarded at compile time | cmd/qrdb/host_adapter_test.go:36 `_ service.Host = (*hostAdapter)(nil)` |
| cmd/qrdb/host_adapter_test.go:TestBootstrapHostAdapter_ImplementsServiceHost | same | cmd/qrdb/host_adapter_test.go:44 `_ service.Host = (*bootstrapHostAdapter)(nil)` |
| cmd/qrdbd/adapters_test.go:TestKVServiceAdapter_ImplementsKVStore | adapter non-nil; the constructor's declared return type is `kvstore.AtomicKVStore` | cmd/qrdbd/kvstore_adapter.go:55 `func newKVServiceAdapter(...) kvstore.AtomicKVStore`; cmd/qrdbd/adapters_test.go:TestKVServiceAdapter_Put_Success |
| cmd/qrdbd/start_coverage_test.go:TestNewKVServiceAdapter_ReturnsNonNil | exact duplicate of the test above (same call, same assertion) | cmd/qrdbd/adapters_test.go:TestKVServiceAdapter_Put_Success |
| cmd/qrdbd/coverage_missing_test.go:TestWebUIHandler_ImplementsHTTPHandler | discarded the result; the declared return type is `http.Handler` (webui.go:23, webui_embed.go:32 under qrdbd_webui) | cmd/qrdbd/coverage_missing_test.go:TestWebUIHandler_ReturnsNil (same call, asserts the result) |

| Kept candidate | Why it stays |
|---|---|
| cmd/qrdb/bindings_server_config_test.go:TestEmbeddedServerBinding_Registry_NotNil | the only check that the embedded binding builds a registry: TestEmbeddedServerBinding_Registry_SameInstance also passes when both calls return nil |
| cmd/qrdb/bindings_setmanager_test.go:TestSetManager_NonNilManager | asserts the stored manager is the one passed (identity), not only non-nil |
| cmd/qrdb/bindings_watch_test.go:TestConvertKeyValue_NonNil | asserts every converted field value (rule 3: behaviour) |
| cmd/qrdb/sql_cmd_test.go:TestSQLCellString_NonNil | asserts the rendering of a non-nil cell (`42` -> "42"), behaviour |
| cmd/qrdbd/kvstore_adapter_guard_test.go NonNilEmptyValue tests | behaviour (brief: keep) |
