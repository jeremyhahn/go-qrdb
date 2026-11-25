# #566 suite audit 5: pkg/api/rest, pkg/dao, pkg/statemachine, sdk/go (rw566u)

Rules as in [566-suite-audit.md](566-suite-audit.md) (ruling 273(12)) and
[566-audit-2.md](566-audit-2.md): a non-nil or implements-interface test
goes when it asserts nothing a named test or a compile-time guard (a
`var _ Iface = ...` line, or a function whose declared return type is the
interface) does not already check. A runtime type assertion on a value
whose dynamic type is the guarded concrete type proves nothing the guard
does not. A test named NonNil that asserts values or identity is behaviour
and stays. No production code changes; no tests added.

| Removed test (file:func) | Behaviour it checked; why removed | Still checked by (file:func or file:line guard) |
|---|---|---|
| pkg/api/rest/server_options_test.go:TestNewSPAHandler_ReturnsNonNil | constructor non-nil; declared return type is `http.Handler` (webui.go:26) | pkg/api/rest/server_options_test.go:TestSPAHandler_ServesExistingAsset |
| pkg/dao/dynamic_entity_test.go:TestDynamicEntity_ImplementsEntity | function-local copy of the production guard | pkg/dao/dynamic_entity.go:96 `var _ Entity = (*DynamicEntity)(nil)` |
| pkg/dao/dynamic_entity_test.go:TestDynamicFieldHashGenerator_ImplementsIDGenerator | `IDGenerator` is embedded in `DeterministicIDGenerator` (id_generator.go:48-49) | pkg/dao/dynamic_entity.go:151 `var _ DeterministicIDGenerator = (*DynamicFieldHashGenerator)(nil)` |
| pkg/statemachine/kv_adapter_test.go:TestStandardAdapter_InterfaceCompliance | function-local copy of the production guards | pkg/statemachine/kv_adapter.go:40-41 |
| pkg/statemachine/kv_adapter_test.go:TestConcurrentAdapter_InterfaceCompliance | same | pkg/statemachine/kv_adapter.go:87-88 |
| pkg/statemachine/kv_adapter_test.go:TestDiskAdapter_InterfaceCompliance | same | pkg/statemachine/kv_adapter.go:180-182 |
| sdk/go/driver/connector_network_test.go:TestNetworkConnector_InterfaceCompliance | same | sdk/go/driver/connector_network.go:32-33 |
| sdk/go/driver/stmt_test.go:TestStmt_InterfaceCompliance | non-nil after an interface conversion of `&stmt{}` and `&networkStmt{}` | sdk/go/driver/stmt.go:26-27 and :159-160 `var _` guards |
| sdk/go/transport/mcp/transport_database_scope_test.go:TestTransportImplementsDatabaseScoper | runtime assertion on `&Transport{}` | sdk/go/transport/mcp/transport.go:68 `var _ transport.DatabaseScoper = (*Transport)(nil)` |
| sdk/go/transport/mcp/transport_guarded_test.go:TestMCP_ImplementsGuardedKVClient | runtime assertion on a `*Transport` | sdk/go/transport/mcp/transport_guarded.go:43 `var _ transport.GuardedKVClient = (*Transport)(nil)` |
| sdk/go/transport/quic/transport_credentials_test.go:TestTransport_ImplementsCredentialClient | interface assignment of a `*Transport` | sdk/go/transport/quic/transport_credentials.go:27 `var _ transport.CredentialClient = (*Transport)(nil)` |
| sdk/go/transport/quic/transport_guarded_test.go:TestTransport_ImplementsGuardedKVClient | runtime assertion on a `*Transport` (started an e2e server for it) | sdk/go/transport/quic/transport_guarded.go:41 `var _ transport.GuardedKVClient = (*Transport)(nil)` |
| sdk/go/transport/unix/transport_guarded_test.go:TestUnixGuarded_ImplementsGuardedKVClient | runtime assertion on a `*Transport` (started a server for it) | sdk/go/transport/unix/transport_guarded.go:32 `var _ transport.GuardedKVClient = (*Transport)(nil)` |

| Kept candidate | Why it stays |
|---|---|
| pkg/api/rest/rest_new_test.go:TestKvToResponse_NonNil | asserts every converted field (behaviour) |
| pkg/dao/generic_dao_compound_test.go:TestCompoundDAO_EntityImplementsCompoundIndexRegisterer | asserts the compound index definitions (behaviour) |
| sdk/go/transport/mcp/transport_test.go:TestTransport_ImplementsClientInterface | drives Put/Get through the `transport.Client` interface (behaviour) |
| sdk/go/testutil/mvcchost/mvcchost_test.go:TestHost_ServicesAreNonNil | the only check in its package that each accessor is wired |
| sdk/go/node/node_test.go:TestBarrierRegistry_NonNilAfterSealEnabled | a seal-enabled, unsealed node exposes its registry (behaviour) |
| sdk/go/transport/embedded/transport_test.go:TestToTransport*_NonNil, embedded/factor_test.go:TestFactorToTransport_NonNilWithMetadata, mcp/transport_coverage_uplift7_test.go:TestMCP_UsageInfoToTransport_NonNil | assert converted values (behaviour) |
| the `...NotNil` empty-slice tests (embedded ReplicaStates, replicaconv FromProto, rest StateHashes) | assert an empty, non-nil list on the wire (behaviour) |
