# #1330 audit — sdk/go/transport/embedded (lane tsa1330o)

Package unit-test time (`go test -tags "$(make -s print-build-tags)" -count=1 -cover ./transport/embedded/` from sdk/go):
- before: 2.546s (go test), 45 s wall incl. compile; coverage 92.3%
- after: TBD

| file | test | verdict | reason | covering test |
|---|---|---|---|---|
| transport_new_coverage_test.go | TestFactorTypeToDomain_AllBranches | moved | only check of mtls/fido2/piv/custom mapping; moved to owning file | factor_test.go |
| transport_new_coverage_test.go | TestFactorTypeFromDomain_AllBranches | moved | only check of reverse mapping | factor_test.go |
| transport_new_coverage_test.go | TestFactorToTransport_NonNilWithMetadata | moved+tightened | only check of field copy; now asserts Type and Label too | factor_test.go |
| transport_new_coverage_test.go | TestFactorToDomain_WithMetadata | moved+tightened | only check of field copy; now asserts Type | factor_test.go |
| transport_new_coverage_test.go | TestMapFactorError_AllBranches | moved | only check of auth→transport factor error mapping | factor_test.go |
| transport_new_coverage_test.go | TestEmbedded_AdminServiceErrors | kept+tightened | only check that admin-service errors surface from Health/Status/Capabilities/Start/StopShard/GetShardState/GetTopology; now asserts errors.Is(errSentinel) | — |
| transport_new_coverage_test.go | TestEmbedded_KVServiceErrors | kept+tightened | only check of KV read-error propagation (Get/List/Scan/Exists-linearizable); mock now returns errSentinel, asserted with errors.Is | — |
| transport_new_coverage_test.go | TestEmbedded_TSServiceErrors | kept+tightened | only check of TS admin-verb error propagation; asserts errSentinel | — |
| transport_new_coverage_test.go | TestEmbedded_VectorServiceErrors | kept+tightened | only check of vector-service error propagation on direct methods; asserts errSentinel | — |
| transport_new_coverage_test.go | TestEmbedded_VectorDispatch_Success | removed | duplicate of the per-verb dispatch success test (superset of its verbs); its one extra assertion (non-nil CreateNamespace response) added there | transport_vector_test.go:TestEmbeddedVector_Dispatch |
| transport_new_coverage_test.go | TestEmbedded_VectorDispatch_ErrorPaths | kept+tightened | only check that vector-service errors surface through Request; asserts errSentinel | — |
| transport_new_coverage_test.go | TestEmbedded_TSDispatch_ErrorPaths | kept+tightened | only check of TS dispatch error propagation; asserts errSentinel | — |
| transport_new_coverage_test.go | TestEmbedded_LeaseDispatch_ErrorPaths | kept+tightened | only check of lease dispatch error propagation; asserts errSentinel | — |
| transport_new_coverage_test.go | TestEmbedded_DispatchNilService_ErrorPaths | kept+tightened | only dispatch-level nil-service check for credential/TS/lease verbs; now asserts *NilServiceError | — |
| transport_new_coverage_test.go | TestEmbedded_KVDispatch_SuccessPaths | removed | Get/List via Request with value assertions already tested | transport_test.go:TestRequest_Get, transport_test.go:TestRequest_List |
| transport_new_coverage_test.go | TestEmbedded_KVDispatch_ErrorPaths | kept+tightened | only check that KV errors surface through Request Get/List; asserts errSentinel | — |
| transport_new_coverage_test.go | TestEmbedded_AdminDispatch_SuccessPaths | kept+tightened | only Request-level check of GetShardState/GetTopology; GetTopology now asserts a non-nil response | — |
| transport_new_coverage_test.go | TestEmbedded_MVCCDispatch_ErrorPaths | removed (merged) | same behaviour (nil service surfaces through Request); cases folded into the nil-service dispatch table, which now asserts *NilServiceError | transport_service_errors_test.go:TestEmbedded_DispatchNilService_ErrorPaths |
| transport_new_coverage_test.go | TestEmbedded_IndexDispatch_ErrorPaths | removed (merged) | as above | transport_service_errors_test.go:TestEmbedded_DispatchNilService_ErrorPaths |
| transport_new_coverage_test.go | TestEmbedded_TransactionBatchWatchDispatch_ErrorPaths | removed (merged) | as above | transport_service_errors_test.go:TestEmbedded_DispatchNilService_ErrorPaths |
| transport_new_coverage_test.go | TestEmbedded_TSQueryDispatch_ErrorPaths | removed (merged) | as above | transport_service_errors_test.go:TestEmbedded_DispatchNilService_ErrorPaths |
| transport_new_coverage_test.go | TestToTransportLease_Nil | rewritten (merged) | only check of nil→nil; folded into one nil-input test in the owning file | transport_test.go:TestToTransportConverters_NilInput |
| transport_new_coverage_test.go | TestToTransportLease_NonNil | removed | lease conversion asserted (ID, TTL) through Request | transport_lease_test.go:TestEmbeddedLease_Dispatch |
| transport_new_coverage_test.go | TestToTransportRetentionPolicy_Nil | rewritten (merged) | as Lease_Nil | transport_test.go:TestToTransportConverters_NilInput |
| transport_new_coverage_test.go | TestToTransportRetentionPolicy_NonNil | removed | asserted only non-nil; policy conversion (DurationNs) asserted end to end | transport_timeseries_test.go:TestEmbeddedTS_GetRetention |
| transport_new_coverage_test.go | TestToTransportNamespaceInfo_Nil | rewritten (merged) | as Lease_Nil | transport_test.go:TestToTransportConverters_NilInput |
| transport_new_coverage_test.go | TestToTransportNamespaceInfo_NonNil | moved+tightened | only check of field copy; now asserts Dimension and VectorCount | transport_test.go:TestToTransportNamespaceInfo_NonNil |
| transport_new_coverage_test.go | TestToTransportVectorData_Nil | rewritten (merged) | as Lease_Nil | transport_test.go:TestToTransportConverters_NilInput |
| transport_new_coverage_test.go | TestToTransportVectorData_NonNil | moved+tightened | asserted only non-nil; now asserts ID and vector | transport_test.go:TestToTransportVectorData_NonNil |
| transport_new_coverage_test.go | TestToTransportRichQueryResponse_NonNil | moved+tightened | only check of AggregatedValue copy; now also Metric and Points | transport_test.go:TestToTransportRichQueryResponse_NonNil |
| transport_new_coverage_test.go | TestEmbedded_GetUserByCertThumbprint_NilService | moved (merged) | only check; merged with the other cert-binding nil-service checks in the owning file | transport_auth_test.go:TestEmbeddedAuth_CertBinding_NilUserService |
| transport_new_coverage_test.go | TestEmbedded_AddCertBinding_NilService | moved (merged) | as above | transport_auth_test.go:TestEmbeddedAuth_CertBinding_NilUserService |
| transport_new_coverage_test.go | TestEmbedded_RemoveCertBinding_NilService | moved (merged) | as above | transport_auth_test.go:TestEmbeddedAuth_CertBinding_NilUserService |
| transport_new_coverage_test.go | TestNilRequestError_Message | moved | only check of the message text; beside the other error-message tests | transport_test.go:TestNilRequestError_Message |
| transport_new_coverage_test.go | TestTranslateError_NilInput | removed | duplicate | transport_test.go:TestTranslateError_Nil |
| transport_new_coverage_test.go | TestTranslateError_VectorNotFound | moved (merged) | only check of the vector/index error mapping; six tests merged as subtests, NamespaceNotFound/Exists now assert the field | transport_test.go:TestTranslateError_VectorAndIndexErrors |
| transport_new_coverage_test.go | TestTranslateError_NamespaceNotFound | moved (merged) | as above | transport_test.go:TestTranslateError_VectorAndIndexErrors |
| transport_new_coverage_test.go | TestTranslateError_NamespaceExists | moved (merged) | as above | transport_test.go:TestTranslateError_VectorAndIndexErrors |
| transport_new_coverage_test.go | TestTranslateError_DimensionMismatch | moved (merged) | as above | transport_test.go:TestTranslateError_VectorAndIndexErrors |
| transport_new_coverage_test.go | TestTranslateError_IndexNotFound | moved (merged) | as above | transport_test.go:TestTranslateError_VectorAndIndexErrors |
| transport_new_coverage_test.go | TestTranslateError_IndexExists | moved (merged) | as above | transport_test.go:TestTranslateError_VectorAndIndexErrors |
| transport_new_coverage_test.go | TestTranslateError_GenericError | removed | duplicate | transport_test.go:TestTranslateError_Generic |
| transport_new_coverage_test.go | TestEmbedded_SealDatabase_NilService | moved (merged) | only check; three barrier nil-service checks merged in the owning file | database_confinement_test.go:TestEmbeddedDatabaseBarrier_NilDatabaseService |
| transport_new_coverage_test.go | TestEmbedded_UnsealDatabase_NilService | moved (merged) | as above | database_confinement_test.go:TestEmbeddedDatabaseBarrier_NilDatabaseService |
| transport_new_coverage_test.go | TestEmbedded_DatabaseBarrierStatus_NilService | moved (merged) | as above | database_confinement_test.go:TestEmbeddedDatabaseBarrier_NilDatabaseService |
| transport_new_coverage_test.go | TestUserAuthRecordToDomain_WithMetadata | moved (merged)+tightened | only check of the wire→domain projection; now asserts every field and that metadata/credential are copied, not aliased | user_auth_factor_test.go:TestUserAuthRecordToDomain |
| transport_new_coverage_test.go | TestUserAuthRecordToDomain_NoMetadata | moved (merged) | as above | user_auth_factor_test.go:TestUserAuthRecordToDomain |
| transport_new_coverage_test.go | TestDomainToUserAuthRecord_Nil | moved (merged)+tightened | only check; nil now asserts the whole zero record | user_auth_factor_test.go:TestDomainToUserAuthRecord |
| transport_new_coverage_test.go | TestDomainToUserAuthRecord_WithMetadata | moved (merged)+tightened | only check; now asserts UserID/Type/Label and metadata copy | user_auth_factor_test.go:TestDomainToUserAuthRecord |
| transport_new_coverage_test.go | TestMergeLoginMetadata_BothEmpty | moved (merged) | only check; four cases merged into one table in the owning file | login_test.go:TestMergeLoginMetadata |
| transport_new_coverage_test.go | TestMergeLoginMetadata_OnlyStored | moved (merged) | as above | login_test.go:TestMergeLoginMetadata |
| transport_new_coverage_test.go | TestMergeLoginMetadata_OnlyReq | moved (merged) | as above | login_test.go:TestMergeLoginMetadata |
| transport_new_coverage_test.go | TestMergeLoginMetadata_ReqWins | moved (merged)+tightened | only check; now asserts the full merged map and that the result does not alias the stored map | login_test.go:TestMergeLoginMetadata |
| transport_new_coverage_test.go | TestEmbedded_CredentialStore_NilService | moved (merged) | only check; four store/retrieve nil-service and closed checks merged in the owning file | transport_credentials_versioned_test.go:TestEmbeddedCredential_StoreRetrieve_NilServiceAndClosed |
| transport_new_coverage_test.go | TestEmbedded_CredentialStore_Closed | moved (merged) | as above | transport_credentials_versioned_test.go:TestEmbeddedCredential_StoreRetrieve_NilServiceAndClosed |
| transport_new_coverage_test.go | TestEmbedded_CredentialRetrieve_NilService | moved (merged) | as above | transport_credentials_versioned_test.go:TestEmbeddedCredential_StoreRetrieve_NilServiceAndClosed |
| transport_new_coverage_test.go | TestEmbedded_CredentialRetrieve_Closed | moved (merged) | as above | transport_credentials_versioned_test.go:TestEmbeddedCredential_StoreRetrieve_NilServiceAndClosed |
| transport_new_coverage_test.go | TestEmbedded_WithEmbeddingService_SetsNilCheck | removed | passed nil, identical to the default transport; same call and assertion as Embed_NilService | transport_absent_service_test.go:TestTransport_AbsentServiceIsAWiringDefect |
| transport_new_coverage_test.go | TestEmbedded_Embed_NilService | moved (merged) | only check; folded into the absent-service wiring test | transport_absent_service_test.go:TestTransport_AbsentServiceIsAWiringDefect |
| transport_new_coverage_test.go | TestEmbedded_BatchEmbed_NilService | moved (merged) | as above | transport_absent_service_test.go:TestTransport_AbsentServiceIsAWiringDefect |
| transport_new_coverage_test.go | TestEmbedded_ListEmbeddingModels_NilService | moved (merged) | as above | transport_absent_service_test.go:TestTransport_AbsentServiceIsAWiringDefect |
| transport_new_coverage_test.go | TestToTransportUsage_Nil | removed | duplicate of FullCoverage's nil case | transport_test.go:TestToTransportUsage |
| transport_new_coverage_test.go | TestToTransportUsage_NonNil | removed | duplicate of FullCoverage's field case | transport_test.go:TestToTransportUsage |
| transport_new_coverage_test.go | TestToTransportUsage_FullCoverage | moved | only check of the usage conversion; beside the other converter tests | transport_test.go:TestToTransportUsage |
| transport_new_coverage_test.go | TestEmbedded_WithRAGService_SetsNilField | rewritten | tautology (passed nil, asserted the zero value); was the only caller of WithRAGService, so replaced by a real assertion that the option installs the service | transport_test.go:TestWithRAGService_InstallsService |
| transport_new_coverage_test.go | TestEmbedded_UserAuthFactor_AllMethods_MissingService | moved+tightened | only check for List/Get/Remove/Rotate/Relabel; asserted only non-empty message, now *MissingUserAuthFactorServiceError (requests carry a FactorID so the service check, not the request check, is what answers) | user_auth_factor_test.go:TestUserAuthFactor_MissingService |
| transport_new_coverage_test.go | TestEmbedded_LoginAuthFactor_NotImplemented | moved (merged) | only check of the no-resolver path | user_auth_factor_test.go:TestUserAuthFactor_MissingService |
| transport_new_coverage_test.go | TestEmbedded_UserAuthFactorClient_ReturnsSelf | moved | only check that the accessor returns the transport (the SDK's type assertion relies on it) | user_auth_factor_test.go:TestUserAuthFactorClient_IsTheTransport |

The surviving error-propagation tests (rows marked kept+tightened, plus their four error mocks) form one cross-service suite with no single owning file; transport_new_coverage_test.go is renamed to transport_service_errors_test.go (git mv) with a header naming what it tests, and its banners dropped.
| transport_rag_coverage_test.go | TestRagChunkConfig_Nil | merged | only check of the no-options path; merged into one test | transport_rag_test.go:TestRagChunkConfig |
| transport_rag_coverage_test.go | TestRagChunkConfig_NonNil | merged+tightened | asserted Strategy/ChunkSize only; now the whole config (Overlap, MinSize too) and the single-option case | transport_rag_test.go:TestRagChunkConfig |
| transport_rag_coverage_test.go | TestEmbedded_RAGIngestDocument_NilService | merged | only check; four per-method nil-service tests merged into one table | transport_rag_test.go:TestEmbeddedRAG_NilService |
| transport_rag_coverage_test.go | TestEmbedded_RAGQuery_NilService | merged | as above | transport_rag_test.go:TestEmbeddedRAG_NilService |
| transport_rag_coverage_test.go | TestEmbedded_RAGDeleteDocument_NilService | merged | as above | transport_rag_test.go:TestEmbeddedRAG_NilService |
| transport_rag_coverage_test.go | TestEmbedded_RAGListDocuments_NilService | merged | as above | transport_rag_test.go:TestEmbeddedRAG_NilService |

transport_rag_coverage_test.go is renamed to transport_rag_test.go (git mv): it is the only test file for the qrdb_vector-tagged transport_rag.go, so it is the owning file.
| transport_vector_coverage_test.go | TestEmbeddedVector_AllMethods_Success | removed | call-without-assert (err == nil only) over the same 20 Transport methods the dispatch handlers call; its one real assertion (IndexType HNSW) moved into Dispatch | transport_vector_test.go:TestEmbeddedVector_Dispatch |
| transport_vector_coverage_test.go | TestEmbeddedVector_Dispatch | kept + tightened | only success-path check of the vector dispatch handlers and their service-to-transport conversion; each verb now asserts a mock-returned field (request echo, counts, IDs, scores, progress) | — |
| transport_vector_coverage_test.go | TestEmbeddedVector_GetNamespaceInfo_DistanceMetric | kept | only check of distance-metric string mapping | — |

Rename: transport_vector_coverage_test.go → transport_vector_test.go (the only vector test file; mockVectorService also backs errVectorService in transport_service_errors_test.go). Banners dropped.

## Remaining-work plan: transport_mvcc_coverage_test.go (analysed in slice 1, not yet edited)

- Remove PutWithLease_WithWriteOption, GetRevision_WithConsistency, GetAtRevision_WithConsistency and Range_WithConsistency. Routing is covered by transport_consistency_test.go:TestEmbedded_{PutWithLease_WithWriteModeUsesAndMode,GetRevision_WithConsistencyUsesConsistencyMethod,GetAtRevision_WithConsistencyUsesConsistencyMethod,Range_WithConsistencyUsesConsistencyMethod}. Both branches share toTransportKV, so conversion stays covered by the _Success tests.
- Tighten GetRevision_Success to also check Value "v" and ModRevision 1.
- Replace Range_WithRangeOptions, which only asserts Count>0 and whose mock ignores the options, with TestToServiceRangeOpts using service.ApplyRangeOptions (Limit, Revision, CountOnly, KeysOnly, sort; zero options give nil).
- Tighten Compact_Success: add a `compacted int64` field to mockMVCCService, set it in Compact, and assert 100.
- Tighten RestoreToRevision_NilRequest to assert the typed error (check what transport.go:1588 returns).
- Tighten TestEmbeddedMVCC_Dispatch per verb: PutWithLease LeaseID 1; GetRevision Key; GetAtRevision ModRevision; Range Revision 77; Compact captured revision; CurrentRevision rev 1.
- Remove Tx_Execute_Success and Tx_ExecuteWithMode, both covered by TestEmbeddedTx_PropagatesTheRevision (plain and write-mode paths, stronger assertion).
- Tighten Tx_Dispatch to assert resp.Succeeded.
- Tighten Tx_ExecuteAllOpTypesAndCompares: capture compares and ops in executeFn and assert every toServiceCompares field (Target, Result, Value, Version, CreateRevision, ModRevision) and the converted response Kvs.
- Batch_Execute_Success and Batch_ExecuteWithMode only assert an error from the fullBatchService stub, which is error-propagation only. Check whether AllMethods_ServiceError or transport_service_errors_test.go covers batch errors; if so, remove; if not, assert the error identity.
- Tighten Batch_ExecuteAllOpTypes so each response carries the op's Key and Value (toServiceOps and toTransportOpResponses), and add RangeEnd and LeaseID checks via a capturing Execute.
- Not yet read: AllMethods_ServiceError, mockMVCCFailing, and the stubReader comment at line 531, which looks like stale filler.
- fullBatchExecSvc is also used by transport_vector_test.go:TestEmbeddedVector_Dispatch, so keep it.
