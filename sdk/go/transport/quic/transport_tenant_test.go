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

package quic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// quicProtoToTenantInfo
// ---------------------------------------------------------------------------

func TestQuicProtoToTenantInfo_Nil(t *testing.T) {
	info := quicProtoToTenantInfo(nil)
	if info.ID != "" || info.Name != "" {
		t.Errorf("expected zero TenantInfo, got %+v", info)
	}
}

func TestQuicProtoToTenantInfo_Full(t *testing.T) {
	now := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	pbResp := &pb.TenantResponse{
		Id:            "acme",
		Name:          "ACME Corp",
		IsolationType: "barrier",
		CreatedAt:     now.Format(time.RFC3339),
		UpdatedAt:     now.Format(time.RFC3339),
		Quotas: &pb.TenantQuotas{
			MaxDatabases:    3,
			MaxRaftGroups:   6,
			MaxCollections:  9,
			MaxKeys:         1000,
			MaxStorageBytes: 1048576,
			MaxUsers:        50,
			MaxRoles:        10,
			MaxCertificates: 20,
			MaxBackups:      5,
			MaxVectorDims:   512,
			MaxTsMetrics:    100,
		},
	}
	got := quicProtoToTenantInfo(pbResp)
	if got.ID != "acme" {
		t.Errorf("ID = %q, want acme", got.ID)
	}
	if got.IsolationType != transport.TenantIsolationBarrier {
		t.Errorf("IsolationType = %q, want %q", got.IsolationType, transport.TenantIsolationBarrier)
	}
	if got.Quotas == nil {
		t.Fatal("expected non-nil Quotas")
	}
	if got.Quotas.MaxDatabases != 3 {
		t.Errorf("MaxDatabases = %d, want 3", got.Quotas.MaxDatabases)
	}
	if got.Quotas.MaxTSMetrics != 100 {
		t.Errorf("MaxTSMetrics = %d, want 100", got.Quotas.MaxTSMetrics)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, now)
	}
	if !got.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, now)
	}
}

func TestQuicProtoToTenantInfo_BadTimestamps(t *testing.T) {
	pbResp := &pb.TenantResponse{
		Id:        "acme",
		CreatedAt: "not-a-time",
		UpdatedAt: "also-not-a-time",
	}
	got := quicProtoToTenantInfo(pbResp)
	if !got.CreatedAt.IsZero() {
		t.Errorf("expected zero CreatedAt, got %v", got.CreatedAt)
	}
	if !got.UpdatedAt.IsZero() {
		t.Errorf("expected zero UpdatedAt, got %v", got.UpdatedAt)
	}
}

// ---------------------------------------------------------------------------
// Closed-state behavior for all TenantClient methods
// ---------------------------------------------------------------------------

func newClosedTenantTransport(t *testing.T) *Transport {
	t.Helper()
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}
	return tr
}

func TestTransport_CreateTenant_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "acme"})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_GetTenant_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	_, err := tr.GetTenant(context.Background(), "acme")
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestTransport_ListTenants_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	_, err := tr.ListTenants(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestTransport_DeleteTenant_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	err := tr.DeleteTenant(context.Background(), "acme")
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestTransport_UpdateTenant_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestTransport_UpdateTenantQuotas_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	_, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestTransport_TenantBarrierInit_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	_, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestTransport_TenantBarrierUnseal_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestTransport_TenantBarrierSeal_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestTransport_TenantBarrierStatus_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	_, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Nil-request handling for the four barrier methods.
//
// A nil pointer must not be dereferenced; instead, a typed RequestError with
// nilTenantBarrierRequestError as Unwrap target must be returned. We test
// against an unconnected (but not closed) transport to bypass the closed
// guard. If the transport later attempts to dial, it will fail — but the
// nil-request guard runs first and short-circuits.
// ---------------------------------------------------------------------------

func newOpenTenantTransport(t *testing.T) *Transport {
	t.Helper()
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestTransport_TenantBarrierInit_NilRequest(t *testing.T) {
	tr := newOpenTenantTransport(t)
	_, err := tr.TenantBarrierInit(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var nre *nilTenantBarrierRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("expected nilTenantBarrierRequestError, got %T: %v", err, err)
	}
	if nre.Method != "TenantBarrierInit" {
		t.Errorf("Method = %q, want TenantBarrierInit", nre.Method)
	}
}

func TestTransport_TenantBarrierUnseal_NilRequest(t *testing.T) {
	tr := newOpenTenantTransport(t)
	err := tr.TenantBarrierUnseal(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var nre *nilTenantBarrierRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("expected nilTenantBarrierRequestError, got %T", err)
	}
}

func TestTransport_TenantBarrierSeal_NilRequest(t *testing.T) {
	tr := newOpenTenantTransport(t)
	err := tr.TenantBarrierSeal(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var nre *nilTenantBarrierRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("expected nilTenantBarrierRequestError, got %T", err)
	}
}

func TestTransport_TenantBarrierStatus_NilRequest(t *testing.T) {
	tr := newOpenTenantTransport(t)
	_, err := tr.TenantBarrierStatus(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var nre *nilTenantBarrierRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("expected nilTenantBarrierRequestError, got %T", err)
	}
}

func TestNilTenantBarrierRequestError_Message(t *testing.T) {
	e := &nilTenantBarrierRequestError{Method: "TenantBarrierInit"}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
	if !bytes.Contains([]byte(e.Error()), []byte("TenantBarrierInit")) {
		t.Errorf("expected error to mention method, got %q", e.Error())
	}
}

// ---------------------------------------------------------------------------
// Wire-format symmetry: the JSON shapes the client sends must round-trip
// into the same fields the server will read. These tests pin the wire format
// against accidental tag drift (e.g. someone renaming json:"tenant_id" to
// json:"tenantID"). The handler tests on the server side cover the server's
// half of the symmetry.
// ---------------------------------------------------------------------------

func TestQuicTenantBarrierInitRequest_WireShape(t *testing.T) {
	in := quicTenantBarrierInitRequest{TenantID: "acme", Threshold: 3, Shares: 5}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var as map[string]any
	if err := json.Unmarshal(b, &as); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if as["tenant_id"] != "acme" {
		t.Errorf("tenant_id = %v, want acme", as["tenant_id"])
	}
	if as["threshold"].(float64) != 3 {
		t.Errorf("threshold = %v, want 3", as["threshold"])
	}
	if as["shares"].(float64) != 5 {
		t.Errorf("shares = %v, want 5", as["shares"])
	}
}

func TestQuicTenantBarrierUnsealRequest_WireShape(t *testing.T) {
	in := quicTenantBarrierUnsealRequest{TenantID: "acme", Key: []byte("k")}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var as map[string]any
	if err := json.Unmarshal(b, &as); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if as["tenant_id"] != "acme" {
		t.Errorf("tenant_id = %v, want acme", as["tenant_id"])
	}
	if _, ok := as["share"]; ok {
		t.Error("the credential unseal must not carry a share field")
	}
	if _, ok := as["key"]; !ok {
		t.Error("expected key field in wire payload")
	}
}

func TestQuicTenantBarrierSealRequest_WireShape(t *testing.T) {
	in := quicTenantBarrierSealRequest{TenantID: "acme"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var as map[string]any
	if err := json.Unmarshal(b, &as); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if as["tenant_id"] != "acme" {
		t.Errorf("tenant_id = %v, want acme", as["tenant_id"])
	}
}

func TestQuicTenantBarrierStatusRequest_WireShape(t *testing.T) {
	in := quicTenantBarrierStatusRequest{TenantID: "acme"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var as map[string]any
	if err := json.Unmarshal(b, &as); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if as["tenant_id"] != "acme" {
		t.Errorf("tenant_id = %v, want acme", as["tenant_id"])
	}
}

func TestQuicTenantBarrierStatusResponse_WireShape(t *testing.T) {
	resp := quicTenantBarrierStatusResponse{
		TenantID:       "acme",
		Sealed:         true,
		Initialized:    true,
		Strategy:       "shamir",
		HardwareBacked: false,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var as map[string]any
	if err := json.Unmarshal(b, &as); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if as["tenant_id"] != "acme" {
		t.Errorf("tenant_id = %v, want acme", as["tenant_id"])
	}
	if as["sealed"] != true {
		t.Errorf("sealed = %v, want true", as["sealed"])
	}
	if as["initialized"] != true {
		t.Errorf("initialized = %v, want true", as["initialized"])
	}
	if as["strategy"] != "shamir" {
		t.Errorf("strategy = %v, want shamir", as["strategy"])
	}
	if as["hardware_backed"] != false {
		t.Errorf("hardware_backed = %v, want false", as["hardware_backed"])
	}
}
