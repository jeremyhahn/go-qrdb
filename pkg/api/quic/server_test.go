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
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TestNewServer_DefaultHandlers
// ---------------------------------------------------------------------------

func TestNewServer_DefaultHandlers(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0")

	if srv.handlers == nil {
		t.Fatal("expected handlers map to be initialized")
	}
	if len(srv.handlers) == 0 {
		t.Fatal("expected handlers map to be populated")
	}
}

func TestNewServer_DefaultHandlers_CoreMethodsPresent(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0")

	coreMethods := []string{
		"kv.Put", "kv.Get", "kv.Delete", "kv.List", "kv.Scan",
		"kv.PutIfAbsent", "kv.CompareAndDelete", "kv.CompareAndSwap",
		"admin.Health", "admin.Status", "admin.Peers",
		"admin.AddMember", "admin.RemoveMember",
		"admin.RequestSnapshot", "admin.Drain", "admin.Capabilities",
	}
	for _, method := range coreMethods {
		if _, ok := srv.handlers[method]; !ok {
			t.Fatalf("expected handler for %q to be registered", method)
		}
	}
}

// ---------------------------------------------------------------------------
// TestNewServer_WithOptions
// ---------------------------------------------------------------------------

func TestNewServer_WithOptions_TLS(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(tlsCfg),
	)
	if srv.tlsConfig == nil {
		t.Fatal("expected tlsConfig to be set")
	}
	if srv.tlsConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("expected MinVersion TLS 1.3, got %d", srv.tlsConfig.MinVersion)
	}
}

func TestNewServer_WithOptions_NilTLS(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(nil),
	)
	if srv.tlsConfig != nil {
		t.Fatal("expected nil tlsConfig when passed nil")
	}
}

func TestNewServer_WithOptions_AllFieldsSet(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0")

	if srv.kvSvc == nil {
		t.Fatal("expected kvSvc to be set")
	}
	if srv.adminSvc == nil {
		t.Fatal("expected adminSvc to be set")
	}
	if srv.addr != "127.0.0.1:0" {
		t.Fatalf("expected addr '127.0.0.1:0', got %q", srv.addr)
	}
}

// ---------------------------------------------------------------------------
// TestServer_Start_NoTLS
// ---------------------------------------------------------------------------

func TestServer_Start_NoTLS(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected error when starting without TLS")
	}

	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StartError, got %T: %v", err, err)
	}

	var tle *tlsRequiredError
	if !errors.As(err, &tle) {
		t.Fatalf("expected underlying *tlsRequiredError, got %T: %v", errors.Unwrap(err), err)
	}
}

func TestServer_Start_NoTLS_ErrorMessage(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected error when starting without TLS")
	}
	msg := err.Error()
	if !strings.Contains(msg, "TLS is required") {
		t.Fatalf("expected TLS required message, got %q", msg)
	}
	if !strings.Contains(msg, "127.0.0.1:0") {
		t.Fatalf("expected address in error message, got %q", msg)
	}
}

// ---------------------------------------------------------------------------
// TestServer_Addr_BeforeStart
// ---------------------------------------------------------------------------

func TestServer_Addr_BeforeStart(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:12345")
	addr := srv.Addr()
	if addr != "127.0.0.1:12345" {
		t.Fatalf("expected configured address before start, got %q", addr)
	}
}

func TestServer_Addr_BeforeStart_DifferentAddr(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "10.0.0.1:9090")
	addr := srv.Addr()
	if addr != "10.0.0.1:9090" {
		t.Fatalf("expected configured address '10.0.0.1:9090', got %q", addr)
	}
}

// ---------------------------------------------------------------------------
// TestServer_Stop_NilListener
// ---------------------------------------------------------------------------

func TestServer_Stop_NilListener(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	// Stop before Start should not panic; listener is nil.
	srv.Stop()
}

func TestServer_Stop_NilListener_Idempotent(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	// Multiple Stop calls on unstarted server should not panic.
	srv.Stop()
	srv.Stop()
}

// ---------------------------------------------------------------------------
// TestBuildHandlers_RegistersAllMethods_NoExtras
// ---------------------------------------------------------------------------

func TestBuildHandlers_RegistersAllMethods_NoExtras(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")

	knownMethods := map[string]bool{
		// KV
		"kv.Put": true, "kv.Get": true, "kv.Delete": true,
		"kv.List": true, "kv.Scan": true,
		// The guarded write verbs (go-qrdb#124).
		"kv.PutIfAbsent": true, "kv.CompareAndDelete": true, "kv.CompareAndSwap": true,
		// Admin
		"admin.Health": true, "admin.Status": true, "admin.Peers": true,
		"admin.AddMember": true, "admin.RemoveMember": true,
		"admin.RequestSnapshot": true, "admin.Drain": true,
		"admin.Capabilities": true,
		"admin.StartShard":   true, "admin.StopShard": true,
		"admin.GetShardState": true, "admin.GetTopology": true,
		"admin.GetNodeCompaction": true, "admin.SetNodeCompaction": true,
		// MVCC
		"mvcc.PutWithLease": true, "mvcc.GetRevision": true,
		"mvcc.GetAtRevision": true, "mvcc.Range": true,
		"mvcc.Compact": true, "mvcc.CurrentRevision": true,
		"mvcc.RestoreToRevision": true,
		// Transaction
		"tx.Transaction": true,
		// Batch
		"batch.Batch": true, "batch.Import": true,
		// Watch
		"watch.Watch": true, "watch.WatchPrefix": true,
		// Lease
		"lease.Grant": true, "lease.Revoke": true,
		"lease.KeepAlive": true, "lease.TimeToLive": true,
		"lease.List": true,
		// Lock
		"lock.Acquire": true, "lock.TryLock": true,
		"lock.Release": true, "lock.Extend": true,
		"lock.Status": true, "lock.List": true,
		// Index
		"index.Create": true, "index.Drop": true,
		"index.Query": true, "index.Scan": true,
		"index.List": true,
		// TimeSeries
		"ts.Write": true, "ts.WriteBatch": true,
		"ts.Query": true, "ts.Aggregate": true,
		"ts.WritePoint": true, "ts.RichQuery": true,
		"ts.DeleteSeries": true, "ts.GetRetention": true,
		"ts.SetRetention": true, "ts.ListMetrics": true,
		// Vector
		"vector.CreateNamespace": true, "vector.DeleteNamespace": true,
		"vector.ListNamespaces": true, "vector.GetNamespaceInfo": true,
		"vector.Upsert": true, "vector.Get": true, "vector.Delete": true,
		"vector.BatchUpsert": true, "vector.BatchDelete": true,
		"vector.List": true, "vector.UpdateMetadata": true,
		"vector.SearchSimilar": true, "vector.SearchByMetadata": true,
		"vector.HybridSearch": true, "vector.SearchByID": true,
		"vector.RebuildIndex": true, "vector.GetRebuildProgress": true,
		"vector.GetIndexStats":    true,
		"vector.CompactNamespace": true, "vector.GetCompactionProgress": true,
		// User management
		"user.Create": true, "user.Get": true, "user.GetByUsername": true,
		"user.GetByEmail":          true,
		"user.GetByCertThumbprint": true, "user.AddCertBinding": true,
		"user.RemoveCertBinding": true, "user.Update": true,
		"user.Delete": true, "user.List": true, "user.Enable": true,
		"user.Disable": true,
		// Role management
		"role.Create": true, "role.Get": true, "role.GetByName": true,
		"role.Update": true, "role.Delete": true, "role.List": true,
		"role.Assign": true, "role.Unassign": true,
		"role.ListPermissions": true, "role.ResolvePermissions": true,
		"role.ReconcileBuiltIn": true,
		// Certificate management
		"cert.Generate": true, "cert.Export": true,
		"cert.Revoke": true, "cert.List": true,
		// Collection
		"collection.Create": true, "collection.Drop": true,
		"collection.List": true, "collection.GetInfo": true,
		"collection.InsertDocument": true, "collection.GetDocument": true,
		"collection.UpdateDocument": true, "collection.DeleteDocument": true,
		"collection.ListDocuments": true,
		// Database management
		"db.Create": true, "db.Get": true, "db.List": true,
		"db.Drop": true, "db.UpdateProtocol": true,
		"db.UpdateEtcd": true, "db.UpdateRaft": true, "db.Scale": true,
		"db.UpdateBarrier": true, "db.UpdateConnection": true, "db.UpdateSQL": true, "db.UpdateCompaction": true,
		"db.Seal": true, "db.Unseal": true, "db.BarrierStatus": true,
		// SQL
		"sql.Query": true,
		// Barrier
		"barrier.Initialize": true, "barrier.Unseal": true,
		"barrier.Seal": true, "barrier.Status": true,
		"barrier.ShamirInitialize": true, "barrier.ShamirUnsealShare": true,
		"barrier.ShamirUnsealShares": true, "barrier.ShamirListShares": true,
		"barrier.ShamirDeleteShare": true, "barrier.ShamirDeleteAllShares": true,
		"barrier.ShamirVerify": true, "barrier.Rekey": true,
		"barrier.RecoveryGenerate": true, "barrier.RecoveryRecover": true,
		"barrier.RecoveryDelete": true, "barrier.RecoveryStatus": true,
		"barrier.RootToken":       true,
		"barrier.OfficerRegister": true, "barrier.OfficerList": true,
		"barrier.ShareClaimBegin": true, "barrier.ShareClaimComplete": true,
		"barrier.OfficerClaimStatus": true, "barrier.RecoveryOfficerRegister": true,
		"barrier.RecoveryShareClaimBegin": true, "barrier.RecoveryShareClaimComplete": true,
		"barrier.ShamirSealVote":   true,
		"barrier.EnableEncryption": true,
		// Ceremony
		"ceremony.Initialize": true, "ceremony.Status": true,
		"ceremony.ClaimCertBegin": true, "ceremony.ClaimCertComplete": true,
		"ceremony.BeginClaimShare": true, "ceremony.CompleteClaimShare": true, "ceremony.SignCSR": true,
		"ceremony.ClaimShareBegin": true, "ceremony.ClaimShareComplete": true,
		"ceremony.SubmitUnsealShare": true,
		// Tenant management
		"tenant.Create": true, "tenant.Get": true, "tenant.List": true,
		"tenant.Update": true, "tenant.Delete": true, "tenant.UpdateQuotas": true,
		"tenant.UpdateEntitlements": true,
		"tenant.ListDeleted":        true,
		"tenant.ReconcileDeleted":   true,
	}

	for method := range srv.handlers {
		if !knownMethods[method] {
			t.Errorf("unexpected handler registered: %q", method)
		}
	}
}

// ---------------------------------------------------------------------------
// TestBuildHandlers_MethodCount_Positive
// ---------------------------------------------------------------------------

func TestBuildHandlers_MethodCount_Positive(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	if len(srv.handlers) <= 0 {
		t.Fatal("expected positive handler count")
	}
}

// ---------------------------------------------------------------------------
// Server field verification tests
// ---------------------------------------------------------------------------

func TestNewServer_KVServiceStored(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0")
	if srv.kvSvc != kvSvc {
		t.Fatal("expected kvSvc to be the same instance passed to NewServer")
	}
}

func TestNewServer_AdminServiceStored(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0")
	if srv.adminSvc != adminSvc {
		t.Fatal("expected adminSvc to be the same instance passed to NewServer")
	}
}

func TestNewServer_ListenerNilBeforeStart(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	if srv.listener != nil {
		t.Fatal("expected listener to be nil before Start")
	}
}

func TestNewServer_ClosedFalseByDefault(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	if srv.closed.Load() {
		t.Fatal("expected closed to be false on new server")
	}
}

// ---------------------------------------------------------------------------
// Server options composition tests
// ---------------------------------------------------------------------------

func TestNewServer_MultipleOptions(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(tlsCfg),
	)
	if srv.tlsConfig != tlsCfg {
		t.Fatal("expected TLS config to match")
	}
}

func TestNewServer_OptionsOrder(t *testing.T) {
	tlsCfg1 := &tls.Config{MinVersion: tls.VersionTLS12}
	tlsCfg2 := &tls.Config{MinVersion: tls.VersionTLS13}
	// Last option wins.
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(tlsCfg1),
		WithTLS(tlsCfg2),
	)
	if srv.tlsConfig != tlsCfg2 {
		t.Fatal("expected last TLS option to win")
	}
	if srv.tlsConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("expected TLS 1.3 from last option, got %d", srv.tlsConfig.MinVersion)
	}
}

// ---------------------------------------------------------------------------
// Start error wrapping verification
// ---------------------------------------------------------------------------

func TestServer_Start_NoTLS_UnwrapChain(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected error when starting without TLS")
	}

	// Verify the full unwrap chain: StartError -> tlsRequiredError
	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StartError, got %T", err)
	}
	if se.Addr != "127.0.0.1:0" {
		t.Fatalf("expected addr '127.0.0.1:0' in StartError, got %q", se.Addr)
	}

	inner := se.Unwrap()
	var tle *tlsRequiredError
	if !errors.As(inner, &tle) {
		t.Fatalf("expected inner error to be *tlsRequiredError, got %T", inner)
	}
}

func TestServer_Start_NoTLS_ErrorsAs(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "10.0.0.1:8443")
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected error when starting without TLS")
	}

	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("expected errors.As to find *StartError, got %T", err)
	}
	if se.Addr != "10.0.0.1:8443" {
		t.Fatalf("expected addr '10.0.0.1:8443', got %q", se.Addr)
	}
}

// ---------------------------------------------------------------------------
// Handler map categories test
// ---------------------------------------------------------------------------

func TestBuildHandlers_KVMethods(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	kvMethods := []string{
		"kv.Put", "kv.Get", "kv.Delete", "kv.List", "kv.Scan",
		"kv.PutIfAbsent", "kv.CompareAndDelete", "kv.CompareAndSwap",
	}
	for _, m := range kvMethods {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("missing KV handler: %q", m)
		}
	}
}

func TestBuildHandlers_AdminMethods(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	adminMethods := []string{
		"admin.Health", "admin.Status", "admin.Peers",
		"admin.AddMember", "admin.RemoveMember",
		"admin.RequestSnapshot", "admin.Drain", "admin.Capabilities",
	}
	for _, m := range adminMethods {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("missing Admin handler: %q", m)
		}
	}
}

func TestBuildHandlers_MVCCMethods(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	mvccMethods := []string{
		"mvcc.PutWithLease", "mvcc.GetRevision", "mvcc.GetAtRevision",
		"mvcc.Range", "mvcc.Compact", "mvcc.CurrentRevision",
		"mvcc.RestoreToRevision",
	}
	for _, m := range mvccMethods {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("missing MVCC handler: %q", m)
		}
	}
}

func TestBuildHandlers_VectorMethods(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	vectorMethods := []string{
		"vector.CreateNamespace", "vector.DeleteNamespace",
		"vector.ListNamespaces", "vector.GetNamespaceInfo",
		"vector.Upsert", "vector.Get", "vector.Delete",
		"vector.BatchUpsert", "vector.BatchDelete",
		"vector.List", "vector.UpdateMetadata",
		"vector.SearchSimilar", "vector.SearchByMetadata",
		"vector.HybridSearch", "vector.SearchByID",
		"vector.RebuildIndex", "vector.GetRebuildProgress",
		"vector.GetIndexStats",
		"vector.CompactNamespace", "vector.GetCompactionProgress",
	}
	for _, m := range vectorMethods {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("missing Vector handler: %q", m)
		}
	}
}

func TestBuildHandlers_UserMethods(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	userMethods := []string{
		"user.Create", "user.Get", "user.GetByUsername", "user.GetByEmail", "user.Update", "user.Delete",
		"user.List", "user.Enable", "user.Disable",
	}
	for _, m := range userMethods {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("missing User handler: %q", m)
		}
	}
}

func TestBuildHandlers_RoleMethods(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	roleMethods := []string{
		"role.Create", "role.Get", "role.GetByName", "role.Update", "role.Delete",
		"role.List", "role.Assign", "role.Unassign", "role.ListPermissions",
		"role.ResolvePermissions",
		"role.ReconcileBuiltIn",
	}
	for _, m := range roleMethods {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("missing Role handler: %q", m)
		}
	}
}

func TestBuildHandlers_CertMethods(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	certMethods := []string{
		"cert.Generate", "cert.Export", "cert.Revoke", "cert.List",
	}
	for _, m := range certMethods {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("missing Cert handler: %q", m)
		}
	}
}

func TestBuildHandlers_LeaseMethods(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	leaseMethods := []string{
		"lease.Grant", "lease.Revoke", "lease.KeepAlive",
		"lease.TimeToLive", "lease.List",
	}
	for _, m := range leaseMethods {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("missing Lease handler: %q", m)
		}
	}
}

func TestBuildHandlers_IndexMethods(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	indexMethods := []string{
		"index.Create", "index.Drop", "index.Query", "index.Scan",
	}
	for _, m := range indexMethods {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("missing Index handler: %q", m)
		}
	}
}

func TestBuildHandlers_TimeSeriesMethods(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	tsMethods := []string{
		"ts.Write", "ts.WriteBatch", "ts.Query", "ts.Aggregate",
	}
	for _, m := range tsMethods {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("missing TimeSeries handler: %q", m)
		}
	}
}

func TestBuildHandlers_WatchMethods(t *testing.T) {
	// Watch is server-streaming and served by handleWatchStream on the stream
	// dispatch path, so it must NOT be registered in the unary handler table.
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	watchMethods := []string{
		"watch.Watch", "watch.WatchPrefix",
	}
	for _, m := range watchMethods {
		if _, ok := srv.handlers[m]; ok {
			t.Errorf("watch method %q must NOT be in the unary dispatch table (it is server-streaming)", m)
		}
	}
}

func TestBuildHandlers_TransactionAndBatchMethods(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	methods := []string{"tx.Transaction", "batch.Batch"}
	for _, m := range methods {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("missing handler: %q", m)
		}
	}
}

// ---------------------------------------------------------------------------
// WithAuthMiddleware
// ---------------------------------------------------------------------------

func TestWithAuthMiddleware_SetsField(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithAuthMiddleware(nil))
	if srv.authMW != nil {
		t.Fatal("expected nil authMW when nil is passed")
	}
}

// ---------------------------------------------------------------------------
// Addr with nil listener
// ---------------------------------------------------------------------------

func TestServer_Addr_ReturnsConfiguredAddr(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "10.0.0.5:9999")
	if got := srv.Addr(); got != "10.0.0.5:9999" {
		t.Fatalf("expected '10.0.0.5:9999', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// logRequest (covers status != StatusOK path)
// ---------------------------------------------------------------------------

func TestLogRequest_OKStatus(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	// logRequest should not panic for any status.
	srv.logRequest("kv.Put", 1, 0, time.Now())
}

func TestLogRequest_ErrorStatus(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	srv.logRequest("kv.Get", 2, 1, time.Now())
}
