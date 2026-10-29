// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package grpc

import (
	"context"
	"errors"
	"net"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// unaryCalls is one call per unary verb, keyed by the method name its
// RequestError carries. Each call's arguments are the smallest the client
// accepts, so the call reaches the wire.
func unaryCalls() map[string]func(context.Context, *Transport) error {
	return map[string]func(context.Context, *Transport) error{
		"QueryAudit": func(ctx context.Context, tr *Transport) error {
			_, err := tr.QueryAudit(ctx, transport.AuditFilter{})
			return err
		},
		"VerifyChain": func(ctx context.Context, tr *Transport) error {
			_, err := tr.VerifyChain(ctx)
			return err
		},
		"VerifyReplicatedChain": func(ctx context.Context, tr *Transport) error {
			_, err := tr.VerifyReplicatedChain(ctx, "acme")
			return err
		},
		"CreateUser": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateUser(ctx, &transport.CreateUserRequest{Username: "u"})
			return err
		},
		"GetUser": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetUser(ctx, 1)
			return err
		},
		"GetUserByUsername": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetUserByUsername(ctx, "acme", "u")
			return err
		},
		"GetUserByEmail": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetUserByEmail(ctx, "acme", "u@db.example.com")
			return err
		},
		"GetUserByCertThumbprint": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetUserByCertThumbprint(ctx, "ab")
			return err
		},
		"AddCertBinding": func(ctx context.Context, tr *Transport) error {
			_, err := tr.AddCertBinding(ctx, 1, "ab", 0)
			return err
		},
		"RemoveCertBinding": func(ctx context.Context, tr *Transport) error {
			_, err := tr.RemoveCertBinding(ctx, 1, "ab")
			return err
		},
		"UpdateUser": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1})
			return err
		},
		"ListUsers": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListUsers(ctx, &transport.ListUsersRequest{})
			return err
		},
		"CreateRole": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateRole(ctx, &transport.CreateRoleRequest{Name: "r"})
			return err
		},
		"GetRole": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetRole(ctx, "r")
			return err
		},
		"UpdateRole": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateRole(ctx, &transport.UpdateRoleRequest{ID: "r"})
			return err
		},
		"ListRoles": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListRoles(ctx, &transport.ListRolesRequest{})
			return err
		},
		"ListPermissions": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListPermissions(ctx)
			return err
		},
		"IssueCert": func(ctx context.Context, tr *Transport) error {
			_, err := tr.IssueCert(ctx, &transport.IssueCertRequest{Username: "u"})
			return err
		},
		"EnrollCert": func(ctx context.Context, tr *Transport) error {
			_, err := tr.EnrollCert(ctx, &transport.EnrollCertRequest{Username: "u"})
			return err
		},
		"IssueDelegation": func(ctx context.Context, tr *Transport) error {
			_, err := tr.IssueDelegation(ctx, &transport.IssueDelegationRequest{})
			return err
		},
		"RevokeCert": func(ctx context.Context, tr *Transport) error {
			_, err := tr.RevokeCert(ctx, "01")
			return err
		},
		"ListCerts": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListCerts(ctx, "acme")
			return err
		},
		"BarrierStatus": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierStatus(ctx)
			return err
		},
		"BarrierClusterUnseal": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierClusterUnseal(ctx)
			return err
		},
		"BarrierHasRecoveryKeys": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierHasRecoveryKeys(ctx)
			return err
		},
		"OfficerList": func(ctx context.Context, tr *Transport) error {
			_, err := tr.OfficerList(ctx)
			return err
		},
		"CreateCollection": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{Database: "app", Name: "c"})
			return err
		},
		"GetCollectionInfo": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetCollectionInfo(ctx, "app", "c")
			return err
		},
		"ListCollections": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListCollections(ctx, "app")
			return err
		},
		"InsertDocument": func(ctx context.Context, tr *Transport) error {
			_, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{Database: "app", Collection: "c", Document: map[string]any{"k": "v"}})
			return err
		},
		"GetDocument": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetDocument(ctx, "app", "c", 1)
			return err
		},
		"ListDocuments": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "app", Collection: "c"})
			return err
		},
		"CreateDatabase": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateDatabase(ctx, transport.CreateDatabaseRequest{Name: "app"})
			return err
		},
		"GetDatabase": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetDatabase(ctx, "app")
			return err
		},
		"ListDatabases": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListDatabases(ctx)
			return err
		},
		"UpdateDatabaseProtocol": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseProtocol(ctx, transport.UpdateDatabaseProtocolRequest{Name: "app"})
			return err
		},
		"UpdateDatabaseEtcd": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseEtcd(ctx, transport.UpdateDatabaseEtcdRequest{Name: "app"})
			return err
		},
		"UpdateDatabaseRaft": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseRaft(ctx, transport.UpdateDatabaseRaftRequest{Name: "app"})
			return err
		},
		"ScaleDatabase": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ScaleDatabase(ctx, transport.ScaleDatabaseRequest{Name: "app"})
			return err
		},
		"UpdateDatabaseBarrier": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseBarrier(ctx, transport.UpdateDatabaseBarrierRequest{Name: "app", Barrier: &transport.DatabaseBarrierConfig{Enabled: true}})
			return err
		},
		"UpdateDatabaseSQL": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseSQL(ctx, transport.UpdateDatabaseSQLRequest{Name: "app"})
			return err
		},
		"UpdateDatabaseCompaction": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseCompaction(ctx, transport.UpdateDatabaseCompactionRequest{Name: "app"})
			return err
		},
		"UpdateDatabaseConnection": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseConnection(ctx, transport.UpdateDatabaseConnectionRequest{Name: "app"})
			return err
		},
		"PutWithLease": func(ctx context.Context, tr *Transport) error {
			_, err := tr.PutWithLease(ctx, "k", []byte("v"), 1)
			return err
		},
		"GetRevision": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetRevision(ctx, "k")
			return err
		},
		"GetAtRevision": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetAtRevision(ctx, "k", 1)
			return err
		},
		"Range": func(ctx context.Context, tr *Transport) error {
			_, err := tr.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{})
			return err
		},
		"CurrentRevision": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CurrentRevision(ctx)
			return err
		},
		"RestoreToRevision": func(ctx context.Context, tr *Transport) error {
			_, err := tr.RestoreToRevision(ctx, &transport.RestoreRevisionRequest{Revision: 1})
			return err
		},
		"CreateTenant": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateTenant(ctx, &transport.CreateTenantRequest{ID: "acme", Name: "Acme"})
			return err
		},
		"ListTrustAnchors": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListTrustAnchors(ctx, "acme")
			return err
		},
		"GetTenant": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetTenant(ctx, "acme")
			return err
		},
		"ListDeletedTenants": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListDeletedTenants(ctx, nil)
			return err
		},
		"ReconcileDeletedTenants": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ReconcileDeletedTenants(ctx)
			return err
		},
		"ListTenants": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListTenants(ctx)
			return err
		},
		"UpdateTenant": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme"})
			return err
		},
		"UpdateTenantQuotas": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateTenantQuotas(ctx, &transport.UpdateTenantQuotasRequest{TenantID: "acme"})
			return err
		},
		"UpdateTenantEntitlements": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateTenantEntitlements(ctx, &transport.UpdateTenantEntitlementsRequest{TenantID: "acme"})
			return err
		},
		"TenantBarrierStatus": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierStatus(ctx, &transport.TenantBarrierStatusRequest{TenantID: "acme"})
			return err
		},
		"TenantBarrierOfficerList": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierOfficerList(ctx, "acme")
			return err
		},
		"TSQuery": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TSQuery(ctx, &transport.TSQueryRequest{Metric: "m"})
			return err
		},
		"TSAggregate": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TSAggregate(ctx, &transport.TSAggregateRequest{Metric: "m", Function: transport.AggAvg})
			return err
		},
		"TSRichQuery": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TSRichQuery(ctx, &transport.TSRichQueryRequest{Metric: "m"})
			return err
		},
		"TSGetRetention": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TSGetRetention(ctx, &transport.TSGetRetentionRequest{Metric: "m"})
			return err
		},
		"TSListMetrics": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TSListMetrics(ctx, &transport.TSListMetricsRequest{})
			return err
		},
		"CreateNamespace": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateNamespace(ctx, &transport.CreateNamespaceRequest{Namespace: "ns", Dimension: 3})
			return err
		},
		"DeleteNamespace": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{Namespace: "ns"})
			return err
		},
		"ListNamespaces": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListNamespaces(ctx, &transport.ListNamespacesRequest{})
			return err
		},
		"GetNamespaceInfo": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns"})
			return err
		},
		"UpsertVector": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpsertVector(ctx, &transport.UpsertVectorRequest{Namespace: "ns", ID: 1, Vector: []float32{1, 2, 3}})
			return err
		},
		"GetVector": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetVector(ctx, &transport.GetVectorRequest{Namespace: "ns", ID: 1})
			return err
		},
		"DeleteVector": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DeleteVector(ctx, &transport.DeleteVectorRequest{Namespace: "ns", ID: 1})
			return err
		},
		"BatchUpsert": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{Namespace: "ns"})
			return err
		},
		"BatchDeleteVectors": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{Namespace: "ns", IDs: []uint64{1}})
			return err
		},
		"ListVectors": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListVectors(ctx, &transport.ListVectorsRequest{Namespace: "ns"})
			return err
		},
		"UpdateMetadata": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1})
			return err
		},
		"SearchSimilar": func(ctx context.Context, tr *Transport) error {
			_, err := tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{1, 2, 3}, K: 1})
			return err
		},
		"SearchByMetadata": func(ctx context.Context, tr *Transport) error {
			_, err := tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{Namespace: "ns"})
			return err
		},
		"HybridSearch": func(ctx context.Context, tr *Transport) error {
			_, err := tr.HybridSearch(ctx, &transport.HybridSearchRequest{Namespace: "ns", QueryVector: []float32{1, 2, 3}, K: 1})
			return err
		},
		"SearchByID": func(ctx context.Context, tr *Transport) error {
			_, err := tr.SearchByID(ctx, &transport.SearchByIDRequest{Namespace: "ns", ID: 1, K: 1})
			return err
		},
		"RebuildIndex": func(ctx context.Context, tr *Transport) error {
			_, err := tr.RebuildIndex(ctx, &transport.RebuildIndexRequest{Namespace: "ns"})
			return err
		},
		"GetRebuildProgress": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{Namespace: "ns"})
			return err
		},
		"GetIndexStats": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetIndexStats(ctx, &transport.GetIndexStatsRequest{Namespace: "ns"})
			return err
		},
		"CompactNamespace": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CompactNamespace(ctx, &transport.CompactNamespaceRequest{Namespace: "ns"})
			return err
		},
		"GetCompactionProgress": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{Namespace: "ns"})
			return err
		},
		"BarrierInitialize": func(ctx context.Context, tr *Transport) error {
			return tr.BarrierInitialize(ctx, &transport.BarrierInitializeRequest{})
		},
		"BarrierUnseal": func(ctx context.Context, tr *Transport) error {
			return tr.BarrierUnseal(ctx, &transport.BarrierUnsealRequest{})
		},
		"BarrierSeal": func(ctx context.Context, tr *Transport) error {
			return tr.BarrierSeal(ctx)
		},
		"BarrierInitializeShamir": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierInitializeShamir(ctx, &transport.BarrierInitializeShamirRequest{})
			return err
		},
		"BarrierEnableEncryption": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierEnableEncryption(ctx, &transport.BarrierEnableEncryptionRequest{})
			return err
		},
		"BarrierGenerateRecoveryKeys": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{})
			return err
		},
		"BarrierDeleteRecoveryKeys": func(ctx context.Context, tr *Transport) error {
			return tr.BarrierDeleteRecoveryKeys(ctx)
		},
		"OfficerRegister": func(ctx context.Context, tr *Transport) error {
			_, err := tr.OfficerRegister(ctx, &transport.OfficerRegisterRequest{})
			return err
		},
		"RecoveryOfficerRegister": func(ctx context.Context, tr *Transport) error {
			_, err := tr.RecoveryOfficerRegister(ctx, &transport.OfficerRegisterRequest{})
			return err
		},
		"ShareClaimBegin": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{})
			return err
		},
		"ShareClaimComplete": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{})
			return err
		},
		"RecoveryShareClaimBegin": func(ctx context.Context, tr *Transport) error {
			_, err := tr.RecoveryShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{})
			return err
		},
		"RecoveryShareClaimComplete": func(ctx context.Context, tr *Transport) error {
			_, err := tr.RecoveryShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{})
			return err
		},
		"BarrierQuorumBegin": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{})
			return err
		},
		"BarrierQuorumSubmit": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{})
			return err
		},
		"BarrierRekeyStart": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{})
			return err
		},
		"BarrierOfficerRevoke": func(ctx context.Context, tr *Transport) error {
			_, err := tr.BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{})
			return err
		},
		"InviteUser": func(ctx context.Context, tr *Transport) error {
			_, err := tr.InviteUser(ctx, &transport.InviteUserRequest{})
			return err
		},
		"RedeemInvitation": func(ctx context.Context, tr *Transport) error {
			_, err := tr.RedeemInvitation(ctx, &transport.RedeemInvitationRequest{})
			return err
		},
		"Query": func(ctx context.Context, tr *Transport) error {
			_, err := tr.Query(ctx, "app", "SELECT 1")
			return err
		},
		"AddTrustAnchors": func(ctx context.Context, tr *Transport) error {
			_, err := tr.AddTrustAnchors(ctx, &transport.AddTrustAnchorsRequest{})
			return err
		},
		"RemoveTrustAnchor": func(ctx context.Context, tr *Transport) error {
			return tr.RemoveTrustAnchor(ctx, "acme", "ab")
		},
		"DeleteTenant": func(ctx context.Context, tr *Transport) error {
			return tr.DeleteTenant(ctx, "acme")
		},
		"TenantBarrierInit": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierInit(ctx, &transport.TenantBarrierInitRequest{})
			return err
		},
		"TenantBarrierUnseal": func(ctx context.Context, tr *Transport) error {
			return tr.TenantBarrierUnseal(ctx, &transport.TenantBarrierUnsealRequest{})
		},
		"TenantBarrierSeal": func(ctx context.Context, tr *Transport) error {
			return tr.TenantBarrierSeal(ctx, &transport.TenantBarrierSealRequest{})
		},
		"TenantBarrierOfficerRegister": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{})
			return err
		},
		"TenantBarrierShareClaimBegin": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{})
			return err
		},
		"TenantBarrierShareClaimComplete": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{})
			return err
		},
		"TenantBarrierQuorumBegin": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{})
			return err
		},
		"TenantBarrierQuorumSubmit": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierQuorumSubmit(ctx, &transport.TenantQuorumSubmitRequest{})
			return err
		},
		"TenantBarrierRekeyStart": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{})
			return err
		},
		"TenantBarrierOfficerRevoke": func(ctx context.Context, tr *Transport) error {
			_, err := tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{})
			return err
		},
		"ListPools": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListPools(ctx)
			return err
		},
		"GetPool": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetPool(ctx, "p")
			return err
		},
		"CreatePool": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{})
			return err
		},
		"UpdatePool": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{})
			return err
		},
		"DeletePool": func(ctx context.Context, tr *Transport) error {
			return tr.DeletePool(ctx, "p")
		},
		"ListPoolTenants": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListPoolTenants(ctx, &transport.ListVolumePoolTenantsRequest{})
			return err
		},
		"Get": func(ctx context.Context, tr *Transport) error {
			_, err := tr.Get(ctx, "k")
			return err
		},
		"PutIfAbsent": func(ctx context.Context, tr *Transport) error {
			_, err := tr.PutIfAbsent(ctx, "k", []byte("v"))
			return err
		},
		"CompareAndDelete": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CompareAndDelete(ctx, "k", []byte("v"))
			return err
		},
		"CompareAndSwap": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CompareAndSwap(ctx, "k", []byte("v"), []byte("w"))
			return err
		},
		"List": func(ctx context.Context, tr *Transport) error {
			_, err := tr.List(ctx, "k")
			return err
		},
		"Scan": func(ctx context.Context, tr *Transport) error {
			_, err := tr.Scan(ctx, "k")
			return err
		},
		"Exists": func(ctx context.Context, tr *Transport) error {
			_, err := tr.Exists(ctx, "k")
			return err
		},
		"Health": func(ctx context.Context, tr *Transport) error {
			_, err := tr.Health(ctx)
			return err
		},
		"Status": func(ctx context.Context, tr *Transport) error {
			_, err := tr.Status(ctx)
			return err
		},
		"Peers": func(ctx context.Context, tr *Transport) error {
			_, err := tr.Peers(ctx)
			return err
		},
		"Capabilities": func(ctx context.Context, tr *Transport) error {
			_, err := tr.Capabilities(ctx)
			return err
		},
		"GetShardState": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetShardState(ctx, 1)
			return err
		},
		"GetTopology": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetTopology(ctx)
			return err
		},
		"ReplicaStates": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ReplicaStates(ctx, 1)
			return err
		},
		"MintJoinToken": func(ctx context.Context, tr *Transport) error {
			_, err := tr.MintJoinToken(ctx, &transport.MintJoinTokenRequest{})
			return err
		},
		"ListPendingJoinRequests": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListPendingJoinRequests(ctx)
			return err
		},
		"EnrollFactor": func(ctx context.Context, tr *Transport) error {
			_, err := tr.EnrollFactor(ctx, &transport.EnrollFactorRequest{})
			return err
		},
		"ChallengeFactor": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{})
			return err
		},
		"ListFactors": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListFactors(ctx, &transport.ListFactorsRequest{})
			return err
		},
		"ExecuteBatch": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ExecuteBatch(ctx, []transport.Op{{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")}})
			return err
		},
		"ImportBegin": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ImportBegin(ctx, transport.ImportOptions{})
			return err
		},
		"ImportCommit": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ImportCommit(ctx, "s")
			return err
		},
		"CeremonyInitialize": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{})
			return err
		},
		"CeremonyStatus": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CeremonyStatus(ctx)
			return err
		},
		"CeremonySignCSR": func(ctx context.Context, tr *Transport) error {
			_, err := tr.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{})
			return err
		},
		"DatabaseBarrierOfficerRegister": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierOfficerRegister(ctx, &transport.DatabaseOfficerRegisterRequest{Database: "app"})
			return err
		},
		"DatabaseBarrierOfficerList": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierOfficerList(ctx, "app")
			return err
		},
		"DatabaseBarrierShamirInitialize": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierShamirInitialize(ctx, &transport.DatabaseShamirInitializeRequest{Database: "app"})
			return err
		},
		"DatabaseBarrierShareClaimBegin": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierShareClaimBegin(ctx, &transport.DatabaseShareClaimBeginRequest{Database: "app"})
			return err
		},
		"DatabaseBarrierShareClaimComplete": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierShareClaimComplete(ctx, &transport.DatabaseShareClaimCompleteRequest{Database: "app"})
			return err
		},
		"DatabaseBarrierQuorumBegin": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierQuorumBegin(ctx, &transport.DatabaseQuorumBeginRequest{Database: "app"})
			return err
		},
		"DatabaseBarrierQuorumSubmit": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierQuorumSubmit(ctx, &transport.DatabaseQuorumSubmitRequest{Database: "app"})
			return err
		},
		"DatabaseBarrierRekeyStart": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierRekeyStart(ctx, &transport.DatabaseRekeyStartRequest{Database: "app"})
			return err
		},
		"DatabaseBarrierOfficerRevoke": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierOfficerRevoke(ctx, &transport.DatabaseOfficerRevokeRequest{Database: "app"})
			return err
		},
		"LockAcquire": func(ctx context.Context, tr *Transport) error {
			_, err := tr.LockAcquire(ctx, &transport.LockAcquireRequest{})
			return err
		},
		"LockTryLock": func(ctx context.Context, tr *Transport) error {
			_, err := tr.LockTryLock(ctx, &transport.LockAcquireRequest{})
			return err
		},
		"LockExtend": func(ctx context.Context, tr *Transport) error {
			_, err := tr.LockExtend(ctx, &transport.LockExtendRequest{})
			return err
		},
		"LockStatus": func(ctx context.Context, tr *Transport) error {
			_, err := tr.LockStatus(ctx, "k")
			return err
		},
		"LockList": func(ctx context.Context, tr *Transport) error {
			_, err := tr.LockList(ctx, "k")
			return err
		},
		"Put": func(ctx context.Context, tr *Transport) error {
			return tr.Put(ctx, "k", []byte("v"))
		},
		"Delete": func(ctx context.Context, tr *Transport) error {
			return tr.Delete(ctx, "k")
		},
		"AddMember": func(ctx context.Context, tr *Transport) error {
			return tr.AddMember(ctx, 1, 2, "127.0.0.1:1", "n2")
		},
		"RemoveMember": func(ctx context.Context, tr *Transport) error {
			return tr.RemoveMember(ctx, 1, 2)
		},
		"PromoteMember": func(ctx context.Context, tr *Transport) error {
			return tr.PromoteMember(ctx, 1, 2)
		},
		"RetireJoinIdentity": func(ctx context.Context, tr *Transport) error {
			return tr.RetireJoinIdentity(ctx, "n2", "gone")
		},
		"RequestSnapshot": func(ctx context.Context, tr *Transport) error {
			return tr.RequestSnapshot(ctx, 1)
		},
		"Drain": func(ctx context.Context, tr *Transport) error {
			return tr.Drain(ctx)
		},
		"StartShard": func(ctx context.Context, tr *Transport) error {
			return tr.StartShard(ctx, &transport.StartShardRequest{})
		},
		"StopShard": func(ctx context.Context, tr *Transport) error {
			return tr.StopShard(ctx, 1)
		},
		"AddObserverMember": func(ctx context.Context, tr *Transport) error {
			return tr.AddObserverMember(ctx, 1, 2, "127.0.0.1:1", "n2")
		},
		"TransferLeadership": func(ctx context.Context, tr *Transport) error {
			return tr.TransferLeadership(ctx, 1, 2)
		},
		"ApproveJoinRequest": func(ctx context.Context, tr *Transport) error {
			return tr.ApproveJoinRequest(ctx, "r", "n2")
		},
		"RejectJoinRequest": func(ctx context.Context, tr *Transport) error {
			return tr.RejectJoinRequest(ctx, "r", "no")
		},
		"VerifyFactor": func(ctx context.Context, tr *Transport) error {
			return tr.VerifyFactor(ctx, &transport.VerifyFactorRequest{})
		},
		"RevokeFactor": func(ctx context.Context, tr *Transport) error {
			return tr.RevokeFactor(ctx, &transport.RevokeFactorRequest{})
		},
		"ImportAppend": func(ctx context.Context, tr *Transport) error {
			return tr.ImportAppend(ctx, "s", 1, []byte("x"))
		},
		"ImportAbort": func(ctx context.Context, tr *Transport) error {
			return tr.ImportAbort(ctx, "s")
		},
		"DropCollection": func(ctx context.Context, tr *Transport) error {
			return tr.DropCollection(ctx, "app", "c")
		},
		"UpdateDocument": func(ctx context.Context, tr *Transport) error {
			return tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{Database: "app", Collection: "c", ID: 1, Document: map[string]any{"k": "v"}})
		},
		"DeleteDocument": func(ctx context.Context, tr *Transport) error {
			return tr.DeleteDocument(ctx, "app", "c", 1)
		},
		"DropDatabase": func(ctx context.Context, tr *Transport) error {
			return tr.DropDatabase(ctx, "app")
		},
		"DatabaseBarrierOfficerUnenrol": func(ctx context.Context, tr *Transport) error {
			return tr.DatabaseBarrierOfficerUnenrol(ctx, &transport.DatabaseOfficerUnenrolRequest{Database: "app"})
		},
		"LockRelease": func(ctx context.Context, tr *Transport) error {
			return tr.LockRelease(ctx, &transport.LockReleaseRequest{})
		},
	}
}

// connectWireServer connects a Transport over bufconn to a server that
// answers every unary RPC with reply: a status error, or, when reply is nil,
// the zero response message. The zero message is what an empty protobuf
// payload decodes to, whatever the response type.
func connectWireServer(t *testing.T, reply error) *Transport {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := googlegrpc.NewServer(googlegrpc.UnknownServiceHandler(func(_ any, stream googlegrpc.ServerStream) error {
		if err := stream.RecvMsg(&emptypb.Empty{}); err != nil {
			return err
		}
		if reply != nil {
			return reply
		}
		return stream.SendMsg(&emptypb.Empty{})
	}))
	go func() { _ = srv.Serve(lis) }()
	conn, err := googlegrpc.NewClient("passthrough:///bufnet",
		googlegrpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
	})
	return &Transport{
		config: &transport.Config{Address: "bufnet"},
		conn:   conn,
		client: pb.NewQRDBServiceClient(conn),
	}
}

// TestGRPC_UnaryServerError_IsARequestErrorNamingTheMethod pins that a
// server failure on every unary verb surfaces as a RequestError carrying the
// verb's own name and the status code, never as a nil error or a panic.
func TestGRPC_UnaryServerError_IsARequestErrorNamingTheMethod(t *testing.T) {
	tr := connectWireServer(t, status.Error(codes.Internal, "boom"))
	for name, call := range unaryCalls() {
		t.Run(name, func(t *testing.T) {
			err := call(context.Background(), tr)
			var re *transport.RequestError
			if !errors.As(err, &re) {
				t.Fatalf("%s = %v, want a RequestError", name, err)
			}
			if re.Method != name || re.StatusCode != int(codes.Internal) {
				t.Fatalf("%s RequestError = {Method: %q, StatusCode: %d}, want {%q, %d}", name, re.Method, re.StatusCode, name, codes.Internal)
			}
		})
	}
}

// TestGRPC_MintJoinToken_NilRequest refuses a nil request with a typed error
// rather than dereferencing it.
func TestGRPC_MintJoinToken_NilRequest(t *testing.T) {
	tr := connectWireServer(t, nil)

	_, err := tr.MintJoinToken(context.Background(), nil)
	var nre *transport.NilRequestError
	var re *transport.RequestError
	if !errors.As(err, &re) || re.Method != "MintJoinToken" || !errors.As(err, &nre) {
		t.Fatalf("MintJoinToken(nil) = %T %v, want RequestError wrapping NilRequestError", err, err)
	}
}

// TestGRPC_ExistsFailure_NamesExists pins that Exists, which is a Get RPC
// that reads NotFound as false, reports any other failure under its own name
// rather than as Get, whatever the status code and read options.
func TestGRPC_ExistsFailure_NamesExists(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.PermissionDenied} {
		tr := connectWireServer(t, status.Error(code, "refused"))
		for _, opts := range [][]transport.ReadOption{nil, {transport.WithConsistency(transport.ConsistencyFollower)}} {
			exists, err := tr.Exists(context.Background(), "k", opts...)
			var re *transport.RequestError
			if !errors.As(err, &re) || re.Method != "Exists" || re.StatusCode != int(code) {
				t.Fatalf("Exists on %s = (%v, %v), want a RequestError {Method: \"Exists\", StatusCode: %d}", code, exists, err, code)
			}
			if exists {
				t.Fatalf("Exists on %s reported true alongside an error", code)
			}
		}
	}
}

// TestGRPC_ExistsNotFound_IsFalseAndGetStaysGet pins the two sides of the
// shared RPC: NotFound is Exists's false, not an error, while Get on the same
// server still reports the miss under Get.
func TestGRPC_ExistsNotFound_IsFalseAndGetStaysGet(t *testing.T) {
	tr := connectWireServer(t, status.Error(codes.NotFound, "no such key"))
	exists, err := tr.Exists(context.Background(), "k")
	if err != nil || exists {
		t.Fatalf("Exists on NotFound = (%v, %v), want (false, nil)", exists, err)
	}
	if _, err := tr.Get(context.Background(), "k"); err == nil {
		t.Fatal("Get on NotFound returned no error")
	}
	tr = connectWireServer(t, status.Error(codes.Internal, "boom"))
	_, err = tr.Get(context.Background(), "k")
	var re *transport.RequestError
	if !errors.As(err, &re) || re.Method != "Get" {
		t.Fatalf("Get failure = %v, want a RequestError naming Get", err)
	}
}

// TestGRPC_UnaryEmptyResponse_Decodes pins that every unary verb but
// GetDocument and MintJoinToken accepts the zero response message: a server that sets no optional field is answered
// with zero values, not a nil dereference.
func TestGRPC_UnaryEmptyResponse_Decodes(t *testing.T) {
	// A document response always carries the document's JSON, so its absence
	// is a decode failure rather than an empty document.
	// A join token response always carries its expiry, and its absence is
	// refused rather than read as a token that never expires.
	refused := map[string]bool{"GetDocument": true, "MintJoinToken": true}
	tr := connectWireServer(t, nil)
	for name, call := range unaryCalls() {
		t.Run(name, func(t *testing.T) {
			err := call(context.Background(), tr)
			if refused[name] {
				var re *transport.RequestError
				if !errors.As(err, &re) || re.Method != name {
					t.Fatalf("%s on the zero response = %v, want a RequestError naming it", name, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s on the zero response = %v, want nil", name, err)
			}
		})
	}
}

// TestGRPC_ClosedTransport_RefusesEveryUnaryVerb pins that a closed
// Transport refuses every unary verb before the wire.
func TestGRPC_ClosedTransport_RefusesEveryUnaryVerb(t *testing.T) {
	tr := connectWireServer(t, nil)
	tr.closed.Store(true)
	for name, call := range unaryCalls() {
		t.Run(name, func(t *testing.T) {
			var ce *transport.ClosedError
			if err := call(context.Background(), tr); !errors.As(err, &ce) {
				t.Fatalf("%s on a closed transport = %v, want a ClosedError", name, err)
			}
		})
	}
}
