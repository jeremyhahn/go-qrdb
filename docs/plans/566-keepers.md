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
