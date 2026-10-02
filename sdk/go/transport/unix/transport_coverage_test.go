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

// Package unix coverage tests fill gaps in closed-state and helper coverage
// that aren't already covered by the round-trip credential / barrier tests.
// They exercise the same closed-check guard at the head of every transport
// method plus the proto-to-transport converters that have no I/O.

package unix

import (
	"bytes"
	"context"
	"errors"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/collectionconv"
)

// ===========================================================================
// transport_tenant.go closed-paths
// ===========================================================================

func TestUnixTenant_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.CreateTenant(ctx, &transport.CreateTenantRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateTenant: %v", err)
	}
	if _, err := tr.GetTenant(ctx, "t"); !errors.As(err, &ce) {
		t.Errorf("GetTenant: %v", err)
	}
	if _, err := tr.ListTenants(ctx); !errors.As(err, &ce) {
		t.Errorf("ListTenants: %v", err)
	}
	if err := tr.DeleteTenant(ctx, "t"); !errors.As(err, &ce) {
		t.Errorf("DeleteTenant: %v", err)
	}
	if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateTenant: %v", err)
	}
	if _, err := tr.UpdateTenantQuotas(ctx, &transport.UpdateTenantQuotasRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateTenantQuotas: %v", err)
	}
}

// ===========================================================================
// transport_ceremony.go closed-paths
// ===========================================================================

func TestUnixCeremony_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyInitialize: %v", err)
	}
	if _, err := tr.CeremonyStatus(ctx); !errors.As(err, &ce) {
		t.Errorf("CeremonyStatus: %v", err)
	}
	if _, err := tr.CeremonyClaimCertBegin(ctx, &transport.ClaimCertBeginRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyClaimCertBegin: %v", err)
	}
	if _, err := tr.CeremonyClaimCertComplete(ctx, &transport.ClaimCertCompleteRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyClaimCertComplete: %v", err)
	}
	if _, err := tr.CeremonyBeginClaimShare(ctx, &transport.BeginClaimShareRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyBeginClaimShare: %v", err)
	}
	if _, err := tr.CeremonyCompleteClaimShare(ctx, &transport.CompleteClaimShareRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyCompleteClaimShare: %v", err)
	}
	if _, err := tr.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonySignCSR: %v", err)
	}
	if _, err := tr.CeremonyEnrollExternalOfficer(ctx, &transport.EnrollExternalOfficerRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyEnrollExternalOfficer: %v", err)
	}
}

// ===========================================================================
// transport_collection.go closed-paths
// ===========================================================================

func TestUnixCollection_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateCollection: %v", err)
	}
	if _, err := tr.GetCollectionInfo(ctx, "db", "c"); !errors.As(err, &ce) {
		t.Errorf("GetCollectionInfo: %v", err)
	}
	if _, err := tr.ListCollections(ctx, "db"); !errors.As(err, &ce) {
		t.Errorf("ListCollections: %v", err)
	}
	if err := tr.DropCollection(ctx, "db", "c"); !errors.As(err, &ce) {
		t.Errorf("DropCollection: %v", err)
	}
	if _, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{}); !errors.As(err, &ce) {
		t.Errorf("InsertDocument: %v", err)
	}
	if _, err := tr.GetDocument(ctx, "db", "c", 1); !errors.As(err, &ce) {
		t.Errorf("GetDocument: %v", err)
	}
	if err := tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDocument: %v", err)
	}
	if err := tr.DeleteDocument(ctx, "db", "c", 1); !errors.As(err, &ce) {
		t.Errorf("DeleteDocument: %v", err)
	}
	if _, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListDocuments: %v", err)
	}
}

// ===========================================================================
// transport_barrier.go closed-paths (the subset not in transport_closed_test.go)
// ===========================================================================

func TestUnixBarrierExtras_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.BarrierInitializeShamir(ctx, &transport.BarrierInitializeShamirRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierInitializeShamir: %v", err)
	}
	if _, err := tr.BarrierUnsealWithShare(ctx, &transport.BarrierUnsealShareRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierUnsealWithShare: %v", err)
	}
	if err := tr.BarrierUnsealWithShares(ctx, &transport.BarrierUnsealSharesRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierUnsealWithShares: %v", err)
	}
	if _, err := tr.BarrierRekey(ctx, &transport.BarrierRekeyRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierRekey: %v", err)
	}
	if _, err := tr.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierGenerateRecoveryKeys: %v", err)
	}
	if err := tr.BarrierRecoverWithKeys(ctx, &transport.BarrierRecoverWithKeysRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierRecoverWithKeys: %v", err)
	}
	if _, err := tr.BarrierGenerateRootToken(ctx, &transport.BarrierGenerateRootTokenRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierGenerateRootToken: %v", err)
	}
}

// ===========================================================================
// transport_watch.go closed-paths
// ===========================================================================

func TestUnixWatch_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.Watch(ctx, "k", transport.WatchOptions{}); !errors.As(err, &ce) {
		t.Errorf("Watch: %v", err)
	}
	if _, err := tr.WatchPrefix(ctx, "p", transport.WatchOptions{}); !errors.As(err, &ce) {
		t.Errorf("WatchPrefix: %v", err)
	}
}

// ===========================================================================
// transport_collection.go protoToCollectionInfo
// ===========================================================================

func TestUnixProtoToCollectionInfo_Valid(t *testing.T) {
	in := &pb.CollectionInfoResponse{
		Name:          "users",
		Database:      "main",
		IdStrategy:    "auto",
		FieldHash:     "_id",
		Schema:        map[string]string{"field": "string"},
		DocumentCount: 42,
		CreatedAt:     "2026-01-01T00:00:00Z",
	}
	out := collectionconv.ToCollectionInfo(in)
	if out == nil {
		t.Fatal("ToCollectionInfo returned nil")
	}
	if out.Name != "users" || out.Database != "main" {
		t.Errorf("identity mismatch: %+v", out)
	}
	if out.IDStrategy != "auto" || out.FieldHash != "_id" {
		t.Errorf("strategy mismatch: %+v", out)
	}
	if out.DocumentCount != 42 {
		t.Errorf("DocumentCount = %d, want 42", out.DocumentCount)
	}
}

// ===========================================================================
// transport_lease.go protoToLeaseInfo
// ===========================================================================

func TestUnixProtoToLeaseInfo_Nil(t *testing.T) {
	if got := protoToLeaseInfo(nil); got != nil {
		t.Errorf("protoToLeaseInfo(nil) = %v, want nil", got)
	}
}

func TestUnixProtoToLeaseInfo_Valid(t *testing.T) {
	in := &pb.Lease{
		Id:           5,
		Ttl:          60,
		GrantedTtl:   120,
		RemainingTtl: 45,
		Keys:         [][]byte{[]byte("a"), []byte("b")},
	}
	out := protoToLeaseInfo(in)
	if out == nil {
		t.Fatal("protoToLeaseInfo returned nil")
	}
	if out.ID != 5 || out.TTL != 60 || out.GrantedTTL != 120 || out.RemainingTTL != 45 {
		t.Errorf("lease mismatch: %+v", out)
	}
	if len(out.Keys) != 2 {
		t.Errorf("keys mismatch: %v", out.Keys)
	}
}

// ===========================================================================
// transport_lock.go protoToLockInfo
// ===========================================================================

func TestUnixProtoToLockInfo_Nil(t *testing.T) {
	if got := protoToLockInfo(nil); got != nil {
		t.Errorf("protoToLockInfo(nil) = %v, want nil", got)
	}
}

func TestUnixProtoToLockInfo_Valid(t *testing.T) {
	in := &pb.LockInfo{
		Key:          "lock-a",
		FencingToken: 99,
		LeaseId:      7,
		HolderId:     "node-1",
		Reentrant:    true,
		AcquireCount: 3,
		Ttl:          30,
		AcquiredAt:   1_700_000_000,
	}
	out := protoToLockInfo(in)
	if out == nil {
		t.Fatal("protoToLockInfo returned nil")
	}
	if out.Key != "lock-a" || out.FencingToken != 99 || out.LeaseID != 7 || out.HolderID != "node-1" {
		t.Errorf("lock identity mismatch: %+v", out)
	}
	if !out.Reentrant || out.AcquireCount != 3 || out.TTL != 30 {
		t.Errorf("lock detail mismatch: %+v", out)
	}
}

// ===========================================================================
// transport_database.go proto conversion helpers
// ===========================================================================

func TestUnixTransportRaftConfigToProto_RoundTrip(t *testing.T) {
	in := &transport.DatabaseRaftConfig{
		ClusterMode:    "multi",
		ReplicaID:      8,
		RaftAddress:    "10.0.0.1:5000",
		Peers:          []string{"8=10.0.0.1:5000"},
		LocalNodeCount: 5,
	}
	out := transportRaftConfigToProto(in)
	if out == nil {
		t.Fatal("nil result")
	}
	if out.GetClusterMode() != "multi" || out.GetReplicaId() != 8 {
		t.Errorf("raft mismatch: %+v", out)
	}
	if out.GetLocalNodeCount() != 5 {
		t.Errorf("LocalNodeCount = %d, want 5", out.GetLocalNodeCount())
	}
}

// ===========================================================================
// transport_mvcc.go protoToMVCCKeyValue
// ===========================================================================

func TestUnixProtoToMVCCKeyValue_Nil(t *testing.T) {
	if got := protoToMVCCKeyValue(nil); got != nil {
		t.Errorf("protoToMVCCKeyValue(nil) = %v, want nil", got)
	}
}

func TestUnixProtoToMVCCKeyValue_Valid(t *testing.T) {
	in := &pb.KeyValue{
		Key:            []byte("k"),
		Value:          []byte("v"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        3,
		LeaseId:        4,
	}
	out := protoToMVCCKeyValue(in)
	if out == nil {
		t.Fatal("protoToMVCCKeyValue returned nil")
	}
	if !bytes.Equal(out.Key, []byte("k")) || !bytes.Equal(out.Value, []byte("v")) {
		t.Errorf("key/value mismatch: %+v", out)
	}
	if out.CreateRevision != 1 || out.ModRevision != 2 || out.Version != 3 || out.LeaseID != 4 {
		t.Errorf("revision mismatch: %+v", out)
	}
}

// ===========================================================================
// transport_transaction.go conversion helpers
// ===========================================================================

func TestUnixCompareToProto_VersionTarget(t *testing.T) {
	c := transport.Compare{
		Key:     []byte("k"),
		Target:  transport.CompareVersion,
		Result:  transport.CompareEqual,
		Version: 5,
	}
	out := compareToProto(c)
	if out == nil {
		t.Fatal("nil result")
	}
	if !bytes.Equal(out.GetKey(), []byte("k")) {
		t.Errorf("Key mismatch")
	}
	if out.GetTarget() != pb.CompareTarget_COMPARE_VERSION {
		t.Errorf("Target = %v, want COMPARE_VERSION", out.GetTarget())
	}
	if out.GetResult() != pb.CompareResult_COMPARE_EQUAL {
		t.Errorf("Result = %v, want COMPARE_EQUAL", out.GetResult())
	}
	if out.GetVersion() != 5 {
		t.Errorf("Version = %d, want 5", out.GetVersion())
	}
}

func TestUnixCompareToProto_CreateRevisionTarget(t *testing.T) {
	c := transport.Compare{
		Target:         transport.CompareCreateRevision,
		Result:         transport.CompareGreater,
		CreateRevision: 7,
	}
	out := compareToProto(c)
	if out.GetCreateRevision() != 7 {
		t.Errorf("CreateRevision = %d, want 7", out.GetCreateRevision())
	}
}

func TestUnixCompareToProto_ModRevisionTarget(t *testing.T) {
	c := transport.Compare{
		Target:      transport.CompareModRevision,
		Result:      transport.CompareLess,
		ModRevision: 11,
	}
	out := compareToProto(c)
	if out.GetModRevision() != 11 {
		t.Errorf("ModRevision = %d, want 11", out.GetModRevision())
	}
}

func TestUnixCompareToProto_ValueTarget(t *testing.T) {
	c := transport.Compare{
		Target: transport.CompareValue,
		Result: transport.CompareNotEqual,
		Value:  []byte("v"),
	}
	out := compareToProto(c)
	if !bytes.Equal(out.GetValue(), []byte("v")) {
		t.Errorf("Value mismatch")
	}
}

func TestUnixOpToProto_AllTypes(t *testing.T) {
	cases := []struct {
		op   transport.Op
		want pb.OpType
	}{
		{transport.Op{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")}, pb.OpType_OP_PUT},
		{transport.Op{Type: transport.OpGet, Key: []byte("k")}, pb.OpType_OP_GET},
		{transport.Op{Type: transport.OpDelete, Key: []byte("k")}, pb.OpType_OP_DELETE},
		{transport.Op{Type: transport.OpRange, Key: []byte("a"), RangeEnd: []byte("z")}, pb.OpType_OP_RANGE},
	}
	for _, tc := range cases {
		out := opToProto(tc.op)
		if out.GetType() != tc.want {
			t.Errorf("Type = %v, want %v", out.GetType(), tc.want)
		}
	}
}

func TestUnixProtoToOpResponse_Valid(t *testing.T) {
	in := &pb.OpResponse{
		Kvs: []*pb.KeyValue{
			{Key: []byte("a"), Value: []byte("1")},
			{Key: []byte("b"), Value: []byte("2")},
		},
		Count:     2,
		Succeeded: true,
	}
	out := protoToOpResponse(in)
	if len(out.Kvs) != 2 {
		t.Fatalf("Kvs len = %d, want 2", len(out.Kvs))
	}
	if out.Count != 2 || !out.Succeeded {
		t.Errorf("count/succeeded mismatch: %+v", out)
	}
}

// ===========================================================================
// transport_timeseries.go conversion helpers
// ===========================================================================

func TestUnixSDKPointToProto_RoundTrip(t *testing.T) {
	in := &transport.TSDataPoint{
		Metric:      "cpu",
		Value:       0.5,
		TimestampNs: 123,
		Tags:        []transport.TSTag{{Key: "host", Value: "h1"}},
	}
	out := sdkPointToProto(in)
	if out.GetMetric() != "cpu" || out.GetValue() != 0.5 || out.GetTimestampNs() != 123 {
		t.Errorf("point mismatch: %+v", out)
	}
	tags := out.GetTags()
	if len(tags) != 1 || tags[0].GetKey() != "host" || tags[0].GetValue() != "h1" {
		t.Errorf("tag mismatch: %+v", tags)
	}
}

func TestUnixSDKTagsToProto_Empty(t *testing.T) {
	if got := sdkTagsToProto(nil); got != nil {
		t.Errorf("sdkTagsToProto(nil) = %v, want nil", got)
	}
}

func TestUnixProtoPointToSDK_RoundTrip(t *testing.T) {
	in := &pb.TSDataPoint{
		Metric:      "mem",
		Value:       42,
		TimestampNs: 1,
		Tags:        []*pb.TSTag{{Key: "k", Value: "v"}},
	}
	out := protoPointToSDK(in)
	if out.Metric != "mem" || out.Value != 42 || out.TimestampNs != 1 {
		t.Errorf("point mismatch: %+v", out)
	}
	if len(out.Tags) != 1 || out.Tags[0].Key != "k" {
		t.Errorf("tag mismatch: %+v", out.Tags)
	}
}

func TestUnixProtoTagsToSDK_Empty(t *testing.T) {
	if got := protoTagsToSDK(nil); got != nil {
		t.Errorf("protoTagsToSDK(nil) = %v, want nil", got)
	}
}

func TestUnixSDKAggFuncToProto_AllValues(t *testing.T) {
	cases := []struct {
		in   transport.AggFunc
		want pb.AggFunc
	}{
		{transport.AggAvg, pb.AggFunc_AGG_AVG},
		{transport.AggSum, pb.AggFunc_AGG_SUM},
		{transport.AggMin, pb.AggFunc_AGG_MIN},
		{transport.AggMax, pb.AggFunc_AGG_MAX},
		{transport.AggCount, pb.AggFunc_AGG_COUNT},
		{transport.AggPercentile, pb.AggFunc_AGG_PERCENTILE},
		{transport.AggNone, pb.AggFunc_AGG_AVG}, // default branch
	}
	for _, tc := range cases {
		if got := sdkAggFuncToProto(tc.in); got != tc.want {
			t.Errorf("sdkAggFuncToProto(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestUnixProtoAggFuncToSDK_AllValues(t *testing.T) {
	cases := []struct {
		in   pb.AggFunc
		want transport.AggFunc
	}{
		{pb.AggFunc_AGG_AVG, transport.AggAvg},
		{pb.AggFunc_AGG_SUM, transport.AggSum},
		{pb.AggFunc_AGG_MIN, transport.AggMin},
		{pb.AggFunc_AGG_MAX, transport.AggMax},
		{pb.AggFunc_AGG_COUNT, transport.AggCount},
		{pb.AggFunc_AGG_PERCENTILE, transport.AggPercentile},
	}
	for _, tc := range cases {
		if got := protoAggFuncToSDK(tc.in); got != tc.want {
			t.Errorf("protoAggFuncToSDK(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// ===========================================================================
// transport_vector.go conversion helpers
// ===========================================================================

func TestUnixFormatVectorID(t *testing.T) {
	if got := formatVectorID(42); got != "42" {
		t.Errorf("formatVectorID(42) = %q, want 42", got)
	}
	if got := formatVectorID(0); got != "0" {
		t.Errorf("formatVectorID(0) = %q, want 0", got)
	}
}

func TestUnixParseVectorID_Valid(t *testing.T) {
	id, err := parseVectorID("42")
	if err != nil {
		t.Fatalf("parseVectorID: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
}

func TestUnixParseVectorID_Empty(t *testing.T) {
	id, err := parseVectorID("")
	if err != nil {
		t.Fatalf("parseVectorID(\"\"): %v", err)
	}
	if id != 0 {
		t.Errorf("id = %d, want 0 for empty input", id)
	}
}

func TestUnixParseVectorID_Invalid(t *testing.T) {
	_, err := parseVectorID("not-a-number")
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
	var ve *vectorIDParseError
	if !errors.As(err, &ve) {
		t.Errorf("expected *vectorIDParseError, got %T: %v", err, err)
	}
}

func TestUnixVectorIDParseError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("boom")
	e := &vectorIDParseError{ID: "bad", Err: inner}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
	if !errors.Is(e, inner) {
		t.Errorf("Unwrap chain broken")
	}
}

func TestUnixMetadataMarshalError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("boom")
	e := &metadataMarshalError{Err: inner}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
	if !errors.Is(e, inner) {
		t.Errorf("Unwrap chain broken")
	}
}

func TestUnixMetadataUnmarshalError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("boom")
	e := &metadataUnmarshalError{Err: inner}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
	if !errors.Is(e, inner) {
		t.Errorf("Unwrap chain broken")
	}
}

func TestUnixVectorDataToProto_Nil(t *testing.T) {
	got, err := vectorDataToProto(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestUnixVectorDataToProto_Valid(t *testing.T) {
	in := &transport.VectorData{
		ID:        7,
		Vector:    []float32{0.1, 0.2, 0.3},
		Metadata:  map[string]interface{}{"city": "nyc"},
		Version:   2,
		CreatedAt: 1_700_000_000,
		UpdatedAt: 1_700_000_100,
	}
	out, err := vectorDataToProto(in)
	if err != nil {
		t.Fatalf("vectorDataToProto: %v", err)
	}
	if out.GetId() != "7" {
		t.Errorf("ID = %q, want 7", out.GetId())
	}
	if len(out.GetVector()) != 3 {
		t.Errorf("vector len mismatch: %v", out.GetVector())
	}
	if out.GetMetadataJson() == "" {
		t.Error("expected non-empty MetadataJson")
	}
}

func TestUnixVectorDataToProto_NoMetadata(t *testing.T) {
	in := &transport.VectorData{ID: 1, Vector: []float32{0.5}}
	out, err := vectorDataToProto(in)
	if err != nil {
		t.Fatalf("vectorDataToProto: %v", err)
	}
	if out.GetMetadataJson() != "" {
		t.Errorf("expected empty MetadataJson, got %q", out.GetMetadataJson())
	}
}

func TestUnixProtoToVectorData_Nil(t *testing.T) {
	got, err := protoToVectorData(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestUnixProtoToVectorData_Valid(t *testing.T) {
	in := &pb.VectorDataProto{
		Id:           "11",
		Vector:       []float32{0.4, 0.5},
		MetadataJson: `{"k":"v"}`,
		Version:      3,
		CreatedAt:    1,
		UpdatedAt:    2,
	}
	out, err := protoToVectorData(in)
	if err != nil {
		t.Fatalf("protoToVectorData: %v", err)
	}
	if out.ID != 11 || out.Version != 3 {
		t.Errorf("id/version mismatch: %+v", out)
	}
	if out.Metadata["k"] != "v" {
		t.Errorf("metadata k = %v, want v", out.Metadata["k"])
	}
}

func TestUnixProtoToVectorData_InvalidID(t *testing.T) {
	in := &pb.VectorDataProto{Id: "not-a-number", Vector: []float32{0.1}}
	_, err := protoToVectorData(in)
	if err == nil {
		t.Fatal("expected error")
	}
	var ve *vectorIDParseError
	if !errors.As(err, &ve) {
		t.Errorf("expected *vectorIDParseError, got %T", err)
	}
}

func TestUnixProtoToVectorData_InvalidMetadata(t *testing.T) {
	in := &pb.VectorDataProto{Id: "1", MetadataJson: "not-json"}
	_, err := protoToVectorData(in)
	if err == nil {
		t.Fatal("expected error for invalid metadata JSON")
	}
}

func TestUnixProtoToSearchResults_Valid(t *testing.T) {
	in := []*pb.VectorSearchResultProto{
		{Id: "1", Score: 0.9, Vector: []float32{0.1}, MetadataJson: `{"a":"b"}`},
		{Id: "2", Score: 0.7, Vector: []float32{0.2}, MetadataJson: ""},
	}
	out, err := protoToSearchResults(in)
	if err != nil {
		t.Fatalf("protoToSearchResults: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].ID != 1 || out[0].Score != 0.9 {
		t.Errorf("[0] mismatch: %+v", out[0])
	}
	if out[1].ID != 2 || out[1].Metadata != nil {
		t.Errorf("[1] mismatch: %+v", out[1])
	}
}

func TestUnixProtoToSearchResults_InvalidID(t *testing.T) {
	in := []*pb.VectorSearchResultProto{{Id: "not-a-number"}}
	_, err := protoToSearchResults(in)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixProtoToSearchResults_InvalidMetadata(t *testing.T) {
	in := []*pb.VectorSearchResultProto{{Id: "1", MetadataJson: "not-json"}}
	_, err := protoToSearchResults(in)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixMarshalStringMetadataJSON_Empty(t *testing.T) {
	got, err := marshalStringMetadataJSON(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestUnixMarshalStringMetadataJSON_Valid(t *testing.T) {
	got, err := marshalStringMetadataJSON(map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `{"k":"v"}` {
		t.Errorf("got %q", got)
	}
}

func TestUnixMarshalInterfaceMetadataJSON_Empty(t *testing.T) {
	got, err := marshalInterfaceMetadataJSON(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestUnixMarshalInterfaceMetadataJSON_Valid(t *testing.T) {
	got, err := marshalInterfaceMetadataJSON(map[string]interface{}{"k": "v"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty JSON")
	}
}

func TestUnixUnmarshalInterfaceMetadataJSON_Empty(t *testing.T) {
	got, err := unmarshalInterfaceMetadataJSON("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil map, got %+v", got)
	}
}

func TestUnixUnmarshalInterfaceMetadataJSON_Valid(t *testing.T) {
	got, err := unmarshalInterfaceMetadataJSON(`{"k":"v"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["k"] != "v" {
		t.Errorf("got %+v, want k=v", got)
	}
}

func TestUnixUnmarshalInterfaceMetadataJSON_Invalid(t *testing.T) {
	_, err := unmarshalInterfaceMetadataJSON("not-json")
	if err == nil {
		t.Fatal("expected error")
	}
	var me *metadataUnmarshalError
	if !errors.As(err, &me) {
		t.Errorf("expected *metadataUnmarshalError, got %T", err)
	}
}

// ===========================================================================
// transport_watch.go protoToWatchEvent + prefixRangeEnd
// ===========================================================================

func TestUnixProtoToWatchEvent_Nil(t *testing.T) {
	if got := protoToWatchEvent(nil); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestUnixProtoToWatchEvent_Put(t *testing.T) {
	in := &pb.Event{
		Type:   pb.EventType_EVENT_PUT,
		Kv:     &pb.KeyValue{Key: []byte("k"), Value: []byte("v")},
		PrevKv: &pb.KeyValue{Key: []byte("k"), Value: []byte("v0")},
	}
	out := protoToWatchEvent(in)
	if out == nil {
		t.Fatal("nil result")
	}
	if out.Type != transport.EventPut {
		t.Errorf("Type = %v, want EventPut", out.Type)
	}
	if out.Kv == nil || !bytes.Equal(out.Kv.Key, []byte("k")) {
		t.Errorf("Kv mismatch: %+v", out.Kv)
	}
	if out.PrevKv == nil || !bytes.Equal(out.PrevKv.Value, []byte("v0")) {
		t.Errorf("PrevKv mismatch: %+v", out.PrevKv)
	}
}

func TestUnixProtoToWatchEvent_Delete(t *testing.T) {
	in := &pb.Event{Type: pb.EventType_EVENT_DELETE, Kv: &pb.KeyValue{Key: []byte("k")}}
	out := protoToWatchEvent(in)
	if out.Type != transport.EventDelete {
		t.Errorf("Type = %v, want EventDelete", out.Type)
	}
}

func TestUnixPrefixRangeEnd_Normal(t *testing.T) {
	got := prefixRangeEnd([]byte("foo"))
	want := []byte("fop")
	if !bytes.Equal(got, want) {
		t.Errorf("prefixRangeEnd(foo) = %q, want %q", got, want)
	}
}

func TestUnixPrefixRangeEnd_Empty(t *testing.T) {
	if got := prefixRangeEnd(nil); got != nil {
		t.Errorf("prefixRangeEnd(nil) = %v, want nil", got)
	}
	if got := prefixRangeEnd([]byte{}); got != nil {
		t.Errorf("prefixRangeEnd([]byte{}) = %v, want nil", got)
	}
}

func TestUnixPrefixRangeEnd_TrailingFF(t *testing.T) {
	// "a\xff" -> "b"
	got := prefixRangeEnd([]byte{'a', 0xff})
	want := []byte{'b'}
	if !bytes.Equal(got, want) {
		t.Errorf("prefixRangeEnd = %x, want %x", got, want)
	}
}

func TestUnixPrefixRangeEnd_AllFF(t *testing.T) {
	got := prefixRangeEnd([]byte{0xff, 0xff})
	if got != nil {
		t.Errorf("prefixRangeEnd(all 0xff) = %v, want nil", got)
	}
}
