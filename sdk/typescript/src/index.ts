// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// ---------------------------------------------------------------------------
// Public API barrel exports for @qrdb/sdk
// ---------------------------------------------------------------------------

// Factory (environment detection and client creation)
export { isDesktop, getClient } from './factory.js';
export type { ClientConfig } from './factory.js';

// Wire-format key transformers (exported for testing and custom transport use)
export { deepSnakeToCamel, deepCamelToSnake, snakeToCamelKey, camelToSnakeKey } from './transform.js';

// Transport implementations
export { RestClient } from './rest.js';
export { WailsClient } from './wails.js';

// Client interfaces
export type {
    Client,
    ConnectionClient,
    KVClient,
    AdminClient,
    MVCCClient,
    TransactionClient,
    BatchClient,
    WatchClient,
    WatchHandle,
    LeaseClient,
    LockClient,
    IndexClient,
    TimeSeriesClient,
    VectorClient,
    UserClient,
    InvitationClient,
    RedeemInvitationOptions,
    RoleClient,
    CertClient,
    DatabaseClient,
    CollectionClient,
    SQLClient,
    SealClient,
    OfficerClient,
    CeremonyClient,
    EmbeddedServerClient,
    DaemonClient,
    TenantClient,
    BackupClient,
    RebalanceClient,
    CredentialClient,
    AuditQueryClient,
    VlogClient,
    VlogSignatureType,
    VlogHeadKey,
    VlogCreateRequest,
    VlogCreateResult,
    VlogListResult,
    VlogAppendRequest,
    VlogAppendResult,
    VlogGetRequest,
    VlogEntry,
    VlogGetResult,
    VlogInclusionRequest,
    VlogInclusionProof,
    VlogConsistencyRequest,
    VlogConsistencyProof,
    VlogHead,
    VlogSignedHead,
    VlogSignHeadResult,
    VlogRotateKeyRequest,
    VlogRotateKeyResult,
    VlogExpireRequest,
    VlogExpireResult,
    ServerConfigClient,
    ConnectionManagerClient,
} from './interfaces.js';

// Error classes
export {
    QRDBError,
    ConfigError,
    ConnectionError,
    NotConnectedError,
    ClosedError,
    TransportError,
    TLSError,
    NotLeaderError,
    KeyNotFoundError,
    RevisionCompactedError,
    RevisionNotFoundError,
    TransactionFailedError,
    LeaseNotFoundError,
    LeaseExpiredError,
    WatchCanceledError,
    IndexExistsError,
    IndexNotFoundError,
    VectorNotFoundError,
    NamespaceNotFoundError,
    NamespaceExistsError,
    DimensionMismatchError,
    UserNotFoundError,
    UserExistsError,
    RoleNotFoundError,
    RoleExistsError,
    CertNotFoundError,
    FeatureDisabledError,
    DesktopOnlyError,
    NotImplementedError,
    ShamirSplitMissingError,
    ShamirSplitMismatchError,
} from './errors.js';

// Request/response types
export type {
    // Health and status
    HealthResponse,
    StatusResponse,
    PeersResponse,
    ShardPeers,
    PeerInfo,
    DrainResponse,
    CapabilitiesResponse,
    TopologyResponse,
    ShardInfo,
    OKResponse,
    ErrorResponse,
    MintJoinTokenResponse,

    // KV
    KVListResponse,
    KVScanResponse,

    // Membership and snapshot
    AddMemberRequest,
    RemoveMemberRequest,
    SnapshotRequest,

    // MVCC
    MVCCKeyValue,
    SortOrder,
    SortTarget,
    RangeOptions,
    RangeResponse,
    PutWithLeaseResponse,
    CurrentRevisionResponse,
    RestoreAction,
    RestoreRevisionRequest,
    RestoreKeyChange,
    RestoreRevisionResult,

    // Transaction
    CompareTarget,
    CompareResult,
    Compare,
    OpType,
    Op,
    OpResponse,
    TransactionRequest,
    TransactionResponse,

    // Batch
    BatchRequest,
    BatchResponse,
    ImportOptions,
    ImportResult,
    ImportError,

    // Watch
    EventType,
    WatchEvent,
    WatchOptions,
    WatchResponse,

    // Lease
    LeaseInfo,
    LeaseListResponse,

    // Lock
    LockInfo,
    LockAcquireRequest,
    LockReleaseRequest,
    LockExtendRequest,
    LockListResponse,

    // Index
    IndexCreateRequest,
    IndexListResponse,
    IndexQueryRequest,
    IndexQueryResponse,
    IndexScanRequest,
    IndexScanEntry,
    IndexScanResponse,

    // Time-series
    TSTag,
    TSDataPoint,
    TSQueryRequest,
    TSQueryResponse,
    AggFunc,
    TSAggregateRequest,
    TSAggResult,
    TSAggregateResponse,
    TSRichQueryRequest,
    TSGroupedResult,
    TSRichQueryResponse,
    TSDeleteRequest,
    TSDeleteResponse,
    TSRetentionPolicy,
    TSGetRetentionRequest,
    TSGetRetentionResponse,
    TSSetRetentionRequest,
    TSListMetricsRequest,
    TSListMetricsResponse,

    // Vector
    DistanceMetric,
    VectorIndexType,
    CreateNamespaceRequest,
    CreateNamespaceResponse,
    DeleteNamespaceRequest,
    ListNamespacesRequest,
    ListNamespacesResponse,
    GetNamespaceInfoRequest,
    NamespaceInfo,
    VectorData,
    UpsertVectorRequest,
    UpsertVectorResponse,
    GetVectorRequest,
    DeleteVectorRequest,
    BatchUpsertRequest,
    BatchUpsertResponse,
    BatchVectorError,
    BatchDeleteVectorsRequest,
    BatchDeleteVectorsResponse,
    ListVectorsRequest,
    ListVectorsResponse,
    UpdateMetadataRequest,
    VectorSearchResult,
    SearchSimilarRequest,
    SearchSimilarResponse,
    SearchByMetadataRequest,
    SearchByMetadataResponse,
    HybridSearchRequest,
    HybridSearchResponse,
    SearchByIDRequest,
    RebuildIndexRequest,
    RebuildIndexResponse,
    GetRebuildProgressRequest,
    RebuildProgress,
    GetIndexStatsRequest,
    IndexStats,
    CompactNamespaceRequest,
    CompactNamespaceResponse,
    GetCompactionProgressRequest,
    CompactionProgress,

    // User management
    User,
    CreateUserRequest,
    UpdateUserRequest,
    AddCertBindingRequest,
    CertBinding,
    ListUsersRequest,
    ListUsersResponse,

    // Invitation self-enrolment
    InviteUserRequest,
    Invitation,
    RedeemInvitationRequest,

    // Role management
    Role,
    CreateRoleRequest,
    UpdateRoleRequest,
    ListRolesRequest,
    ListRolesResponse,
    AssignRoleRequest,
    UnassignRoleRequest,
    PermissionCategory,
    ListPermissionsResponse,

    // Certificate management
    IssueCertRequest,
    EnrollCertRequest,
    EnrolledCert,
    IssuedCertWithKey,
    ExportCertRequest,
    ExportCertResponse,
    CertRecord,
    ListCertsResponse,

    // Embedded server
    EmbeddedConfig,
    EmbeddedRaftConfig,
    SealMode,
    EmbeddedSealConfig,
    EmbeddedAuditConfig,

    // Collection management
    CollectionInfo,
    CreateCollectionRequest,
    ListCollectionsResponse,

    // Document management
    DocumentResponse,
    InsertDocumentRequest,
    InsertDocumentResponse,
    UpdateDocumentRequest,
    ListDocumentsRequest,
    ListDocumentsResponse,

    // Database management
    DatabaseInfo,
    CreateDatabaseRequest,
    DatabaseProtocolConfig,
    DatabaseSQLConfig,
    CompactionMode,
    DatabaseCompactionConfig,
    UpdateDatabaseCompactionRequest,
    NodeCompactionBody,
    SQLConcurrency,
    DatabaseRaftConfig,
    StaticDiscoveryConfig,
    MulticastDiscoveryConfig,
    DNSDiscoveryConfig,
    TokenDiscoveryConfig,
    DatabaseRaftInfo,
    StaticDiscoveryInfo,
    MulticastDiscoveryInfo,
    DNSDiscoveryInfo,
    TokenDiscoveryInfo,
    DatabaseBarrierConfig,
    DatabaseBarrierStatus,
    DatabaseBarrierRequest,
    DatabaseConnectionConfig,
    UpdateDatabaseProtocolRequest,
    UpdateDatabaseSQLRequest,
    UpdateDatabaseRaftRequest,
    UpdateDatabaseBarrierRequest,
    UpdateDatabaseConnectionRequest,
    ListDatabasesResponse,
    DaemonStatus,

    // SQL
    SQLQueryResult,

    // Seal
    SealStatus,
    ClusterSeal,
    ClusterMigration,
    ClusterMigrationNode,
    BootstrapResult,
    UnsealProgress,
    EnableEncryptionRequest,
    EnableEncryptionResponse,

    // Share-holder administration
    HolderRole,
    QuorumDomain,
    FactorPolicy,
    HolderFactorEnrolment,
    OfficerRegisterRequest,
    OfficerRegisterResponse,
    OfficerStatus,
    OfficerRevokeRequest,
    QuorumRound,
    RekeyStartRequest,
    ShamirInitializeRequest,
    TenantOfficerRegisterRequest,
    TenantOfficerRevokeRequest,
    DatabaseOfficerRegisterRequest,
    DatabaseOfficerRevokeRequest,
    DatabaseOfficerUnenrolRequest,
    DatabaseRekeyStartRequest,
    DatabaseShamirInitializeRequest,
    DatabaseShamirInitializeResponse,
    TenantRekeyStartRequest,

    // Ceremony
    CeremonyOfficerRequest,
    CeremonyInitializeRequest,
    CeremonyHolderCertificate,
    CeremonyInitializeResponse,
    CeremonyStatusResponse,
    CeremonySignCSRRequest,
    CeremonySignCSRResponse,

    // Tenant management
    TenantIsolationType,
    TenantInfo,
    CreateTenantRequest,
    CreateTenantResponse,
    GetTenantResponse,
    ListTenantsResponse,
    UpdateTenantRequest,
    UpdateTenantResponse,
    TenantBarrierInitRequest,
    TenantBarrierInitResponse,
    TenantBarrierUnsealRequest,
    TenantBarrierStatusResponse,
    TenantQuotas,
    TenantEntitlements,
    UpdateTenantQuotasRequest,
    UpdateTenantQuotasResponse,
    UpdateTenantEntitlementsRequest,
    UpdateTenantEntitlementsResponse,
    TenantAdminCredential,
    TrustAnchor,
    TrustAnchorList,

    // Server config
    ConfigInitializedResponse,

    // Backup and restore
    BackupRequest,
    BackupInfo,
    RestoreRequest,
    RestoreResult,
    ShardError,
    VerifyResult,
    ListBackupsResponse,

    // Rebalance
    ShardStatsResponse,
    RebalancePlanRequest,
    RebalanceMove,
    RebalancePlan,
    RebalanceResult,
    NodeLoad,
    ImbalanceReport,

    // Credentials
    CredentialVersionInfo,
    CredentialRotateResult,

    // Connection manager
    ConnectionProfile,
    ConnectionState,
    ExternalConnectionConfig,

} from './types.js';

// Audit event retrieval (Phase 6 compliance)
export { AuditClient } from './audit.js';
export type { AuditEvent, AuditFilter, AuditPage } from './audit.js';

// Auth factor operations (Phase 3 compliance: PKCS#11, PIV, FIDO2)
export { AuthFactorClient, AuthFactorError } from './authfactor.js';
export type {
    Enrollment,
    EnrollFactorRequest,
    EnrollFactorResponse,
    ChallengeFactorRequest,
    ChallengeFactorResponse,
    VerifyFactorRequest,
    ListFactorsRequest,
    ListFactorsResponse,
    RevokeFactorRequest,
} from './authfactor.js';
