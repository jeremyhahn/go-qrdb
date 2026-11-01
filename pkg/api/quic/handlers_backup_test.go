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
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mockBackupServiceQuic is a service.BackupServicer whose methods delegate
// to the matching func field, or return a fixed default when it is nil.
type mockBackupServiceQuic struct {
	createFn  func(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error)
	listFn    func(ctx context.Context) ([]service.BackupInfo, error)
	getFn     func(ctx context.Context, id string) (*service.BackupInfo, error)
	deleteFn  func(ctx context.Context, id string) error
	restoreFn func(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error)
	verifyFn  func(ctx context.Context, id string) (*service.VerifyResult, error)
}

var _ service.BackupServicer = (*mockBackupServiceQuic)(nil)

func (m *mockBackupServiceQuic) CreateBackup(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, opts)
	}
	return &service.BackupInfo{ID: "backup-001", Destination: opts.Destination}, nil
}

func (m *mockBackupServiceQuic) ListBackups(ctx context.Context) ([]service.BackupInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []service.BackupInfo{{ID: "backup-001"}}, nil
}

func (m *mockBackupServiceQuic) GetBackup(ctx context.Context, id string) (*service.BackupInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &service.BackupInfo{ID: id}, nil
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
	return &service.RestoreResult{BackupID: opts.BackupID}, nil
}

func (m *mockBackupServiceQuic) VerifyBackup(ctx context.Context, id string) (*service.VerifyResult, error) {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, id)
	}
	return &service.VerifyResult{Valid: true}, nil
}

// backupHandlers names every backup handler; payloadless marks the one that
// ignores its payload and so has no decode failure.
var backupHandlers = map[string]struct {
	call        func(*Server, context.Context, []byte) ([]byte, error)
	payloadless bool
}{
	"Create":  {call: (*Server).handleBackupCreate},
	"List":    {call: (*Server).handleBackupList, payloadless: true},
	"Get":     {call: (*Server).handleBackupGet},
	"Delete":  {call: (*Server).handleBackupDelete},
	"Restore": {call: (*Server).handleBackupRestore},
	"Verify":  {call: (*Server).handleBackupVerify},
}

func TestBackupInfoToProto_Nil(t *testing.T) {
	if got := backupInfoToProto(nil); got != nil {
		t.Fatalf("backupInfoToProto(nil) = %v, want nil", got)
	}
}

func TestBackupInfoToProto_EveryField(t *testing.T) {
	ts := time.Unix(1700000000, 0)
	got := backupInfoToProto(&service.BackupInfo{
		ID:            "b1",
		Timestamp:     ts,
		Size:          8192,
		ShardCount:    4,
		Status:        service.BackupStatusComplete,
		Checksum:      "deadbeef",
		Destination:   "/tmp/b",
		Compression:   "zstd",
		Encryption:    "aes-256-gcm",
		Metadata:      map[string]string{"tag": "v1"},
		Duration:      3 * time.Second,
		ErrorMessage:  "partial",
		KeyProviderID: "kp-1",
	})
	want := &pb.BackupInfo{
		Id:            "b1",
		TimestampNs:   ts.UnixNano(),
		Size:          8192,
		ShardCount:    4,
		Status:        string(service.BackupStatusComplete),
		Checksum:      "deadbeef",
		Destination:   "/tmp/b",
		Compression:   "zstd",
		Encryption:    "aes-256-gcm",
		Metadata:      map[string]string{"tag": "v1"},
		DurationNs:    int64(3 * time.Second),
		ErrorMessage:  "partial",
		KeyProviderId: "kp-1",
	}
	if !proto.Equal(got, want) {
		t.Fatalf("backupInfoToProto = %v, want %v", got, want)
	}
}

func TestBackupHandlers_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	for name, h := range backupHandlers {
		t.Run(name, func(t *testing.T) {
			_, err := h.call(srv, context.Background(), nil)
			var snc *ServiceNotConfiguredError
			if !errors.As(err, &snc) || snc.Service != "backup" {
				t.Fatalf("expected ServiceNotConfiguredError{backup}, got %T(%v)", err, err)
			}
		})
	}
}

func TestBackupHandlers_UnmarshalError(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(&mockBackupServiceQuic{}))
	for name, h := range backupHandlers {
		if h.payloadless {
			continue
		}
		t.Run(name, func(t *testing.T) {
			_, err := h.call(srv, context.Background(), []byte{0xff, 0xfe})
			var ue *UnmarshalError
			if !errors.As(err, &ue) {
				t.Fatalf("expected *UnmarshalError, got %T(%v)", err, err)
			}
		})
	}
}

func TestBackupHandlers_ServiceError(t *testing.T) {
	svcErr := errors.New("backup service failed")
	svc := &mockBackupServiceQuic{
		createFn:  func(context.Context, service.BackupOptions) (*service.BackupInfo, error) { return nil, svcErr },
		listFn:    func(context.Context) ([]service.BackupInfo, error) { return nil, svcErr },
		getFn:     func(context.Context, string) (*service.BackupInfo, error) { return nil, svcErr },
		deleteFn:  func(context.Context, string) error { return svcErr },
		restoreFn: func(context.Context, service.RestoreOptions) (*service.RestoreResult, error) { return nil, svcErr },
		verifyFn:  func(context.Context, string) (*service.VerifyResult, error) { return nil, svcErr },
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	for name, h := range backupHandlers {
		t.Run(name, func(t *testing.T) {
			if _, err := h.call(srv, context.Background(), nil); !errors.Is(err, svcErr) {
				t.Fatalf("expected the service error, got %v", err)
			}
		})
	}
}

func TestHandleBackupCreate_Success(t *testing.T) {
	var got service.BackupOptions
	svc := &mockBackupServiceQuic{
		createFn: func(_ context.Context, opts service.BackupOptions) (*service.BackupInfo, error) {
			got = opts
			return &service.BackupInfo{ID: "backup-001", Destination: opts.Destination}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupCreateRequest{
		Destination:   "/tmp/backup",
		Compression:   "gzip",
		Encryption:    "aes-256-gcm",
		KeyProviderId: "kp-1",
		KeyRef:        "key-1",
		Metadata:      map[string]string{"tag": "v1"},
		ShardIds:      []uint64{1, 2},
	})
	resp, err := srv.handleBackupCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBackupCreate: %v", err)
	}
	if got.Destination != "/tmp/backup" || got.Compression != "gzip" || got.Encryption != "aes-256-gcm" ||
		got.KeyProviderID != "kp-1" || got.KeyRef != "key-1" || got.Metadata["tag"] != "v1" ||
		len(got.ShardIDs) != 2 || got.ShardIDs[0] != 1 || got.ShardIDs[1] != 2 {
		t.Fatalf("service got options %+v", got)
	}
	var out pb.BackupInfo
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetId() != "backup-001" || out.GetDestination() != "/tmp/backup" {
		t.Fatalf("response = %v, want backup-001 at /tmp/backup", &out)
	}
}

func TestHandleBackupList_Success(t *testing.T) {
	svc := &mockBackupServiceQuic{
		listFn: func(context.Context) ([]service.BackupInfo, error) {
			return []service.BackupInfo{{ID: "b1"}, {ID: "b2"}}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	resp, err := srv.handleBackupList(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleBackupList: %v", err)
	}
	var out pb.BackupListResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetBackups()) != 2 || out.GetBackups()[0].GetId() != "b1" || out.GetBackups()[1].GetId() != "b2" {
		t.Fatalf("backups = %v, want [b1 b2]", out.GetBackups())
	}
}

func TestHandleBackupGet_Success(t *testing.T) {
	var gotID string
	svc := &mockBackupServiceQuic{
		getFn: func(_ context.Context, id string) (*service.BackupInfo, error) {
			gotID = id
			return &service.BackupInfo{ID: id, Checksum: "deadbeef"}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupGetRequest{Id: "backup-001"})
	resp, err := srv.handleBackupGet(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBackupGet: %v", err)
	}
	if gotID != "backup-001" {
		t.Fatalf("service got id %q, want backup-001", gotID)
	}
	var out pb.BackupInfo
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetId() != "backup-001" || out.GetChecksum() != "deadbeef" {
		t.Fatalf("response = %v, want backup-001 with checksum deadbeef", &out)
	}
}

func TestHandleBackupDelete_Success(t *testing.T) {
	var gotID string
	svc := &mockBackupServiceQuic{
		deleteFn: func(_ context.Context, id string) error {
			gotID = id
			return nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupDeleteRequest{Id: "backup-001"})
	if _, err := srv.handleBackupDelete(context.Background(), payload); err != nil {
		t.Fatalf("handleBackupDelete: %v", err)
	}
	if gotID != "backup-001" {
		t.Fatalf("service got id %q, want backup-001", gotID)
	}
}

func TestHandleBackupRestore_Success(t *testing.T) {
	var got service.RestoreOptions
	svc := &mockBackupServiceQuic{
		restoreFn: func(_ context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
			got = opts
			return &service.RestoreResult{
				BackupID:       opts.BackupID,
				ShardsRestored: 1,
				Duration:       time.Second,
				Errors:         []service.ShardError{{ShardID: 3, Message: "timeout"}},
			}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupRestoreRequest{
		BackupId:         "backup-001",
		TargetShards:     []uint64{1, 3},
		ValidateFirst:    true,
		DecryptionKeyRef: "key-1",
		AllowPlaintext:   true,
	})
	resp, err := srv.handleBackupRestore(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBackupRestore: %v", err)
	}
	if got.BackupID != "backup-001" || len(got.TargetShards) != 2 || got.TargetShards[1] != 3 ||
		!got.ValidateFirst || got.DecryptionKeyRef != "key-1" || !got.AllowPlaintext {
		t.Fatalf("service got options %+v", got)
	}
	var out pb.BackupRestoreResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := &pb.BackupRestoreResponse{
		BackupId:       "backup-001",
		ShardsRestored: 1,
		DurationNs:     int64(time.Second),
		Errors:         []*pb.ShardError{{ShardId: 3, Message: "timeout"}},
	}
	if !proto.Equal(&out, want) {
		t.Fatalf("response = %v, want %v", &out, want)
	}
}

func TestHandleBackupVerify_Success(t *testing.T) {
	var gotID string
	svc := &mockBackupServiceQuic{
		verifyFn: func(_ context.Context, id string) (*service.VerifyResult, error) {
			gotID = id
			return &service.VerifyResult{
				Valid:            false,
				ChecksumMatch:    false,
				ExpectedChecksum: "abc123",
				ActualChecksum:   "def456",
				Errors:           []string{"checksum mismatch"},
			}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithBackupService(svc))
	payload, _ := proto.Marshal(&pb.BackupVerifyRequest{Id: "backup-001"})
	resp, err := srv.handleBackupVerify(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBackupVerify: %v", err)
	}
	if gotID != "backup-001" {
		t.Fatalf("service got id %q, want backup-001", gotID)
	}
	var out pb.BackupVerifyResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := &pb.BackupVerifyResponse{
		ExpectedChecksum: "abc123",
		ActualChecksum:   "def456",
		Errors:           []string{"checksum mismatch"},
	}
	if !proto.Equal(&out, want) {
		t.Fatalf("response = %v, want %v", &out, want)
	}
}
