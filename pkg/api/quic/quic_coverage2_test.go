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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockBackupService (QUIC package – separate from grpc package)
// ---------------------------------------------------------------------------

type mockBackupServiceQuic struct {
	createFn  func(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error)
	listFn    func(ctx context.Context) ([]service.BackupInfo, error)
	getFn     func(ctx context.Context, id string) (*service.BackupInfo, error)
	deleteFn  func(ctx context.Context, id string) error
	restoreFn func(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error)
	verifyFn  func(ctx context.Context, id string) (*service.VerifyResult, error)
}

func (m *mockBackupServiceQuic) CreateBackup(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, opts)
	}
	return &service.BackupInfo{
		ID:          "backup-001",
		Timestamp:   time.Now(),
		ShardCount:  2,
		Destination: opts.Destination,
	}, nil
}

func (m *mockBackupServiceQuic) ListBackups(ctx context.Context) ([]service.BackupInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []service.BackupInfo{{ID: "backup-001", Timestamp: time.Now()}}, nil
}

func (m *mockBackupServiceQuic) GetBackup(ctx context.Context, id string) (*service.BackupInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &service.BackupInfo{ID: id, Timestamp: time.Now()}, nil
}

func (m *mockBackupServiceQuic) DeleteBackup(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockBackupServiceQuic) RestoreBackup(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
	if m.restoreFn != nil {
		return m.restoreFn(ctx, opts)
	}
	return &service.RestoreResult{
		BackupID:       opts.BackupID,
		ShardsRestored: 2,
		Duration:       time.Second,
	}, nil
}

func (m *mockBackupServiceQuic) VerifyBackup(ctx context.Context, id string) (*service.VerifyResult, error) {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, id)
	}
	return &service.VerifyResult{
		Valid:            true,
		ChecksumMatch:    true,
		ExpectedChecksum: "abc123",
		ActualChecksum:   "abc123",
	}, nil
}

// ---------------------------------------------------------------------------
// mockCustodianCeremonyService extends mockCeremonyService to provide real
// custodian-ceremony behaviour.
// ---------------------------------------------------------------------------

type mockCustodianCeremonyService struct {
	mockCeremonyService
}

// ---------------------------------------------------------------------------
// Server option tests (WithBackupService / WithRebalanceService /
// WithEmbedding / WithRAGService – 0% covered)
// ---------------------------------------------------------------------------

func TestWithBackupService_QUIC(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	if srv.backupSvc == nil {
		t.Fatal("expected backupSvc to be set")
	}
}

func TestWithRebalanceService_QUIC(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(nil))
	if srv.rebalanceSvc != nil {
		t.Fatal("expected rebalanceSvc to be nil when nil is passed")
	}
}

func TestWithEmbedding_QUIC(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(nil))
	if srv.embeddingSvc != nil {
		t.Fatal("expected embeddingSvc to be nil when nil is passed")
	}
}

func TestWithRAGService_QUIC(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(nil))
	if srv.ragSvc != nil {
		t.Fatal("expected ragSvc to be nil when nil is passed")
	}
}

// ---------------------------------------------------------------------------
// backupInfoToProto (direct unit test for quic package version)
// ---------------------------------------------------------------------------

func TestBackupInfoToProtoQUIC_Nil(t *testing.T) {
	if got := backupInfoToProto(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestBackupInfoToProtoQUIC_Full(t *testing.T) {
	ts := time.Unix(1700000000, 0)
	b := &service.BackupInfo{
		ID:           "b1",
		Timestamp:    ts,
		Size:         8192,
		ShardCount:   4,
		Status:       service.BackupStatusComplete,
		Checksum:     "deadbeef",
		Destination:  "/tmp/b",
		Compression:  "zstd",
		Encryption:   "none",
		Metadata:     map[string]string{"tag": "v1"},
		Duration:     3 * time.Second,
		ErrorMessage: "",
	}
	out := backupInfoToProto(b)
	if out.GetId() != "b1" {
		t.Errorf("expected id 'b1', got %q", out.GetId())
	}
	if out.GetTimestampNs() != ts.UnixNano() {
		t.Errorf("unexpected timestamp_ns")
	}
	if out.GetShardCount() != 4 {
		t.Errorf("expected shard_count=4, got %d", out.GetShardCount())
	}
	if out.GetChecksum() != "deadbeef" {
		t.Errorf("expected checksum 'deadbeef', got %q", out.GetChecksum())
	}
}

// ---------------------------------------------------------------------------
// handleBackupCreate
// ---------------------------------------------------------------------------

func TestHandleBackupCreate_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.BackupCreateRequest{Destination: "/tmp"})
	_, err := srv.handleBackupCreate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleBackupCreate_InvalidPayload(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	_, err := srv.handleBackupCreate(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleBackupCreate_Success(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupCreateRequest{
		Destination: "/tmp/backup",
		Compression: "gzip",
	})
	resp, err := srv.handleBackupCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBackupCreate failed: %v", err)
	}
	var out pb.BackupInfo
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.GetId() != "backup-001" {
		t.Fatalf("expected id 'backup-001', got %q", out.GetId())
	}
}

func TestHandleBackupCreate_ServiceError(t *testing.T) {
	svc := &mockBackupServiceQuic{
		createFn: func(_ context.Context, _ service.BackupOptions) (*service.BackupInfo, error) {
			return nil, errors.New("create failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupCreateRequest{Destination: "/tmp"})
	_, err := srv.handleBackupCreate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleBackupList
// ---------------------------------------------------------------------------

func TestHandleBackupList_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	_, err := srv.handleBackupList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleBackupList_Success(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	resp, err := srv.handleBackupList(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleBackupList failed: %v", err)
	}
	var out pb.BackupListResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetBackups()) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(out.GetBackups()))
	}
}

func TestHandleBackupList_ServiceError(t *testing.T) {
	svc := &mockBackupServiceQuic{
		listFn: func(_ context.Context) ([]service.BackupInfo, error) {
			return nil, errors.New("list failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	_, err := srv.handleBackupList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleBackupGet
// ---------------------------------------------------------------------------

func TestHandleBackupGet_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.BackupGetRequest{Id: "x"})
	_, err := srv.handleBackupGet(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleBackupGet_InvalidPayload(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	_, err := srv.handleBackupGet(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleBackupGet_Success(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupGetRequest{Id: "backup-001"})
	resp, err := srv.handleBackupGet(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBackupGet failed: %v", err)
	}
	var out pb.BackupInfo
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetId() != "backup-001" {
		t.Fatalf("expected id 'backup-001', got %q", out.GetId())
	}
}

func TestHandleBackupGet_ServiceError(t *testing.T) {
	svc := &mockBackupServiceQuic{
		getFn: func(_ context.Context, _ string) (*service.BackupInfo, error) {
			return nil, errors.New("not found")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupGetRequest{Id: "missing"})
	_, err := srv.handleBackupGet(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleBackupDelete
// ---------------------------------------------------------------------------

func TestHandleBackupDelete_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.BackupDeleteRequest{Id: "x"})
	_, err := srv.handleBackupDelete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleBackupDelete_InvalidPayload(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	_, err := srv.handleBackupDelete(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleBackupDelete_Success(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupDeleteRequest{Id: "backup-001"})
	_, err := srv.handleBackupDelete(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBackupDelete failed: %v", err)
	}
}

func TestHandleBackupDelete_ServiceError(t *testing.T) {
	svc := &mockBackupServiceQuic{
		deleteFn: func(_ context.Context, _ string) error {
			return errors.New("delete failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupDeleteRequest{Id: "backup-001"})
	_, err := srv.handleBackupDelete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleBackupRestore
// ---------------------------------------------------------------------------

func TestHandleBackupRestore_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.BackupRestoreRequest{BackupId: "x"})
	_, err := srv.handleBackupRestore(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleBackupRestore_InvalidPayload(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	_, err := srv.handleBackupRestore(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleBackupRestore_Success(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupRestoreRequest{
		BackupId:     "backup-001",
		TargetShards: []uint64{1, 2},
	})
	resp, err := srv.handleBackupRestore(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBackupRestore failed: %v", err)
	}
	var out pb.BackupRestoreResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetBackupId() != "backup-001" {
		t.Fatalf("expected backup_id 'backup-001', got %q", out.GetBackupId())
	}
}

func TestHandleBackupRestore_WithShardErrors(t *testing.T) {
	svc := &mockBackupServiceQuic{
		restoreFn: func(_ context.Context, _ service.RestoreOptions) (*service.RestoreResult, error) {
			return &service.RestoreResult{
				BackupID:       "b1",
				ShardsRestored: 1,
				Errors: []service.ShardError{
					{ShardID: 3, Message: "timeout"},
				},
			}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupRestoreRequest{BackupId: "b1"})
	resp, err := srv.handleBackupRestore(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBackupRestore failed: %v", err)
	}
	var out pb.BackupRestoreResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetErrors()) != 1 {
		t.Fatalf("expected 1 shard error, got %d", len(out.GetErrors()))
	}
}

func TestHandleBackupRestore_ServiceError(t *testing.T) {
	svc := &mockBackupServiceQuic{
		restoreFn: func(_ context.Context, _ service.RestoreOptions) (*service.RestoreResult, error) {
			return nil, errors.New("restore failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupRestoreRequest{BackupId: "b"})
	_, err := srv.handleBackupRestore(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleBackupVerify
// ---------------------------------------------------------------------------

func TestHandleBackupVerify_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.BackupVerifyRequest{Id: "x"})
	_, err := srv.handleBackupVerify(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleBackupVerify_InvalidPayload(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	_, err := srv.handleBackupVerify(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleBackupVerify_Success(t *testing.T) {
	svc := &mockBackupServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupVerifyRequest{Id: "backup-001"})
	resp, err := srv.handleBackupVerify(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBackupVerify failed: %v", err)
	}
	var out pb.BackupVerifyResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.GetValid() {
		t.Fatal("expected valid=true")
	}
}

func TestHandleBackupVerify_ServiceError(t *testing.T) {
	svc := &mockBackupServiceQuic{
		verifyFn: func(_ context.Context, _ string) (*service.VerifyResult, error) {
			return nil, errors.New("verify failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupVerifyRequest{Id: "x"})
	_, err := srv.handleBackupVerify(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleCredentialStore / handleCredentialRetrieve (0% covered)
// ---------------------------------------------------------------------------

func TestHandleCredentialStore_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.CredentialStoreRequest{Name: "k", Value: []byte("v")})
	_, err := srv.handleCredentialStore(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCredentialStore_InvalidPayload(t *testing.T) {
	srv := newCredentialTestServer(t, &mockCredentialService{})
	_, err := srv.handleCredentialStore(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleCredentialStore_Success(t *testing.T) {
	var captured struct {
		name  string
		value []byte
	}
	svc := &mockCredentialService{
		storeFn: func(_ context.Context, name string, value []byte) error {
			captured.name = name
			captured.value = value
			return nil
		},
	}
	srv := newCredentialTestServer(t, svc)
	payload, _ := proto.Marshal(&pb.CredentialStoreRequest{Name: "db-pass", Value: []byte("s3cret")})
	resp, err := srv.handleCredentialStore(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleCredentialStore failed: %v", err)
	}
	if resp != nil {
		t.Fatalf("expected nil response, got %q", resp)
	}
	if captured.name != "db-pass" {
		t.Errorf("expected name 'db-pass', got %q", captured.name)
	}
	if string(captured.value) != "s3cret" {
		t.Errorf("expected value 's3cret', got %q", captured.value)
	}
}

func TestHandleCredentialStore_ServiceError(t *testing.T) {
	svc := &mockCredentialService{
		storeFn: func(_ context.Context, _ string, _ []byte) error {
			return errors.New("store failed")
		},
	}
	srv := newCredentialTestServer(t, svc)
	payload, _ := proto.Marshal(&pb.CredentialStoreRequest{Name: "k", Value: []byte("v")})
	_, err := srv.handleCredentialStore(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandleCredentialRetrieve_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.CredentialRetrieveRequest{Name: "k"})
	_, err := srv.handleCredentialRetrieve(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCredentialRetrieve_InvalidPayload(t *testing.T) {
	srv := newCredentialTestServer(t, &mockCredentialService{})
	_, err := srv.handleCredentialRetrieve(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleCredentialRetrieve_Success(t *testing.T) {
	svc := &mockCredentialService{
		retrieveFn: func(_ context.Context, name string) ([]byte, error) {
			if name != "db-pass" {
				return nil, errors.New("not found")
			}
			return []byte("s3cret"), nil
		},
	}
	srv := newCredentialTestServer(t, svc)
	payload, _ := proto.Marshal(&pb.CredentialRetrieveRequest{Name: "db-pass"})
	resp, err := srv.handleCredentialRetrieve(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleCredentialRetrieve failed: %v", err)
	}
	var out pb.CredentialRetrieveResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(out.GetValue()) != "s3cret" {
		t.Fatalf("expected value 's3cret', got %q", out.GetValue())
	}
}

func TestHandleCredentialRetrieve_ServiceError(t *testing.T) {
	svc := &mockCredentialService{
		retrieveFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, errors.New("retrieve failed")
		},
	}
	srv := newCredentialTestServer(t, svc)
	payload, _ := proto.Marshal(&pb.CredentialRetrieveRequest{Name: "k"})
	_, err := srv.handleCredentialRetrieve(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleAddCertBinding (handlers_users.go – 0% covered)
// ---------------------------------------------------------------------------

func TestHandleAddCertBinding_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, err := json.Marshal(addCertBindingWireRequest{UserID: 1, Thumbprint: "sha256:abc"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	_, err = srv.handleAddCertBinding(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleAddCertBinding_InvalidPayload(t *testing.T) {
	mockUS := &mockUserService{}
	srv := NewServer(nil, nil, "localhost:0", WithUserService(mockUS))
	_, err := srv.handleAddCertBinding(context.Background(), []byte("not-json"))
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleAddCertBinding_Success(t *testing.T) {
	mockUS := &mockUserService{}
	srv := NewServer(nil, nil, "localhost:0", WithUserService(mockUS))

	payload, _ := json.Marshal(addCertBindingWireRequest{
		UserID:       101,
		Thumbprint:   "sha256:deadbeef",
		NotAfterUnix: 1800000000,
	})
	resp, err := srv.handleAddCertBinding(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleAddCertBinding failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// TestHandleAddCertBinding_NilUser verifies that handleAddCertBinding
// returns a nil response (no json.Marshal error) when AddCertBinding returns
// a nil user (mock default). The handler marshals the nil → empty JSON object,
// which is valid.
func TestHandleAddCertBinding_NilUser(t *testing.T) {
	mockUS := &mockUserService{}
	srv := NewServer(nil, nil, "localhost:0", WithUserService(mockUS))
	payload, _ := json.Marshal(addCertBindingWireRequest{
		UserID:     42,
		Thumbprint: "sha256:abc",
	})
	// The mock returns (nil, nil); handler should gracefully marshal the nil user.
	_, err := srv.handleAddCertBinding(context.Background(), payload)
	// nil user is valid — the response may be a nil or empty JSON object.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
