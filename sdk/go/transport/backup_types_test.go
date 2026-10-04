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

package transport

import (
	"encoding/json"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// BackupRequest serialization tests
// ---------------------------------------------------------------------------

func TestBackupRequest_JSON_Roundtrip(t *testing.T) {
	req := BackupRequest{
		Destination:   "/backups/daily",
		Compression:   CompressionZstd,
		Encryption:    EncryptionAES256,
		KeyProviderID: "software",
		KeyRef:        "/etc/qrdb/backup.key",
		Metadata:      map[string]string{"env": "production"},
		ShardIDs:      []uint64{1, 2, 3},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal BackupRequest: %v", err)
	}

	var decoded BackupRequest
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal BackupRequest: %v", err)
	}

	if decoded.Destination != req.Destination {
		t.Fatalf("expected destination %q, got %q", req.Destination, decoded.Destination)
	}
	if decoded.Compression != req.Compression {
		t.Fatalf("expected compression %q, got %q", req.Compression, decoded.Compression)
	}
	if decoded.Encryption != req.Encryption {
		t.Fatalf("expected encryption %q, got %q", req.Encryption, decoded.Encryption)
	}
	if decoded.KeyProviderID != req.KeyProviderID {
		t.Fatalf("expected key provider id %q, got %q", req.KeyProviderID, decoded.KeyProviderID)
	}
	if decoded.KeyRef != req.KeyRef {
		t.Fatalf("expected key ref %q, got %q", req.KeyRef, decoded.KeyRef)
	}
	if decoded.Metadata["env"] != "production" {
		t.Fatalf("expected metadata env=production, got %q", decoded.Metadata["env"])
	}
	if len(decoded.ShardIDs) != 3 {
		t.Fatalf("expected 3 shard IDs, got %d", len(decoded.ShardIDs))
	}
}

func TestBackupRequest_JSON_MinimalFields(t *testing.T) {
	req := BackupRequest{
		Destination: "/backups/minimal",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal BackupRequest: %v", err)
	}

	var decoded BackupRequest
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal BackupRequest: %v", err)
	}

	if decoded.Destination != "/backups/minimal" {
		t.Fatalf("expected destination /backups/minimal, got %q", decoded.Destination)
	}
	if decoded.Compression != "" {
		t.Fatalf("expected empty compression, got %q", decoded.Compression)
	}
	if decoded.Encryption != "" {
		t.Fatalf("expected empty encryption, got %q", decoded.Encryption)
	}
}

// ---------------------------------------------------------------------------
// BackupInfo serialization tests
// ---------------------------------------------------------------------------

func TestBackupInfo_JSON_Roundtrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	info := BackupInfo{
		ID:           "backup-123",
		Timestamp:    now,
		Size:         1048576,
		ShardCount:   3,
		Status:       BackupStatusComplete,
		Checksum:     "abc123def456",
		Destination:  "/backups/daily",
		Compression:  CompressionSnappy,
		Encryption:   EncryptionNone,
		Metadata:     map[string]string{"env": "staging"},
		Duration:     5 * time.Second,
		ErrorMessage: "",
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal BackupInfo: %v", err)
	}

	var decoded BackupInfo
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal BackupInfo: %v", err)
	}

	if decoded.ID != info.ID {
		t.Fatalf("expected ID %q, got %q", info.ID, decoded.ID)
	}
	if !decoded.Timestamp.Equal(info.Timestamp) {
		t.Fatalf("expected timestamp %v, got %v", info.Timestamp, decoded.Timestamp)
	}
	if decoded.Size != info.Size {
		t.Fatalf("expected size %d, got %d", info.Size, decoded.Size)
	}
	if decoded.ShardCount != info.ShardCount {
		t.Fatalf("expected shard count %d, got %d", info.ShardCount, decoded.ShardCount)
	}
	if decoded.Status != BackupStatusComplete {
		t.Fatalf("expected status complete, got %s", decoded.Status)
	}
	if decoded.Checksum != info.Checksum {
		t.Fatalf("expected checksum %q, got %q", info.Checksum, decoded.Checksum)
	}
}

// ---------------------------------------------------------------------------
// RestoreRequest serialization tests
// ---------------------------------------------------------------------------

func TestRestoreRequest_JSON_Roundtrip(t *testing.T) {
	req := RestoreRequest{
		BackupID:         "backup-456",
		TargetShards:     []uint64{1, 3},
		ValidateFirst:    true,
		DecryptionKeyRef: "/etc/qrdb/backup.key",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal RestoreRequest: %v", err)
	}

	var decoded RestoreRequest
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal RestoreRequest: %v", err)
	}

	if decoded.BackupID != req.BackupID {
		t.Fatalf("expected backup ID %q, got %q", req.BackupID, decoded.BackupID)
	}
	if len(decoded.TargetShards) != 2 {
		t.Fatalf("expected 2 target shards, got %d", len(decoded.TargetShards))
	}
	if !decoded.ValidateFirst {
		t.Fatal("expected ValidateFirst to be true")
	}
	if decoded.DecryptionKeyRef != req.DecryptionKeyRef {
		t.Fatalf("expected decryption key ref %q, got %q", req.DecryptionKeyRef, decoded.DecryptionKeyRef)
	}
}

func TestRestoreRequest_JSON_EmptyTargetShards(t *testing.T) {
	req := RestoreRequest{
		BackupID: "backup-789",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal RestoreRequest: %v", err)
	}

	var decoded RestoreRequest
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal RestoreRequest: %v", err)
	}

	if decoded.BackupID != "backup-789" {
		t.Fatalf("expected backup ID backup-789, got %q", decoded.BackupID)
	}
	if decoded.TargetShards != nil {
		t.Fatalf("expected nil target shards, got %v", decoded.TargetShards)
	}
}

// ---------------------------------------------------------------------------
// RestoreResult serialization tests
// ---------------------------------------------------------------------------

func TestRestoreResult_JSON_Roundtrip(t *testing.T) {
	result := RestoreResult{
		BackupID:       "backup-abc",
		ShardsRestored: 3,
		Duration:       10 * time.Second,
		Errors: []ShardError{
			{ShardID: 5, Message: "shard corrupted"},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal RestoreResult: %v", err)
	}

	var decoded RestoreResult
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal RestoreResult: %v", err)
	}

	if decoded.BackupID != result.BackupID {
		t.Fatalf("expected backup ID %q, got %q", result.BackupID, decoded.BackupID)
	}
	if decoded.ShardsRestored != 3 {
		t.Fatalf("expected 3 shards restored, got %d", decoded.ShardsRestored)
	}
	if len(decoded.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(decoded.Errors))
	}
	if decoded.Errors[0].ShardID != 5 {
		t.Fatalf("expected shard ID 5, got %d", decoded.Errors[0].ShardID)
	}
}

// ---------------------------------------------------------------------------
// VerifyResult serialization tests
// ---------------------------------------------------------------------------

func TestVerifyResult_JSON_Roundtrip(t *testing.T) {
	result := VerifyResult{
		Valid:            true,
		ChecksumMatch:    true,
		ExpectedChecksum: "deadbeef",
		ActualChecksum:   "deadbeef",
		Errors:           nil,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal VerifyResult: %v", err)
	}

	var decoded VerifyResult
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal VerifyResult: %v", err)
	}

	if !decoded.Valid {
		t.Fatal("expected valid to be true")
	}
	if !decoded.ChecksumMatch {
		t.Fatal("expected checksum match to be true")
	}
	if decoded.ExpectedChecksum != "deadbeef" {
		t.Fatalf("expected checksum deadbeef, got %q", decoded.ExpectedChecksum)
	}
}

func TestVerifyResult_JSON_WithErrors(t *testing.T) {
	result := VerifyResult{
		Valid:            false,
		ChecksumMatch:    false,
		ExpectedChecksum: "aaa",
		ActualChecksum:   "bbb",
		Errors:           []string{"checksum mismatch", "shard 2 missing"},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal VerifyResult: %v", err)
	}

	var decoded VerifyResult
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal VerifyResult: %v", err)
	}

	if decoded.Valid {
		t.Fatal("expected valid to be false")
	}
	if len(decoded.Errors) != 2 {
		t.Fatalf("expected 2 errors, got %d", len(decoded.Errors))
	}
}

// ---------------------------------------------------------------------------
// Backup error message tests
// ---------------------------------------------------------------------------

func TestBackupTransportErrors_Messages(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "BackupNotFoundError",
			err:      &BackupNotFoundError{ID: "abc"},
			expected: "transport: backup not found: abc",
		},
		{
			name:     "BackupCreateError",
			err:      &BackupCreateError{Err: &BackupEmptyDestinationError{}},
			expected: "transport: backup creation failed: transport: backup destination must not be empty",
		},
		{
			name:     "BackupRestoreError",
			err:      &BackupRestoreError{BackupID: "x", Err: &BackupNotFoundError{ID: "x"}},
			expected: "transport: backup restore failed for x: transport: backup not found: x",
		},
		{
			name:     "BackupEmptyDestinationError",
			err:      &BackupEmptyDestinationError{},
			expected: "transport: backup destination must not be empty",
		},
		{
			name:     "BackupEmptyIDError",
			err:      &BackupEmptyIDError{},
			expected: "transport: backup ID must not be empty",
		},
		{
			name:     "BackupChecksumMismatchError",
			err:      &BackupChecksumMismatchError{Expected: "aaa", Actual: "bbb"},
			expected: "transport: backup checksum mismatch: expected aaa, got bbb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Constant value tests
// ---------------------------------------------------------------------------

func TestBackupStatusConstants(t *testing.T) {
	statuses := []BackupStatus{
		BackupStatusPending,
		BackupStatusRunning,
		BackupStatusComplete,
		BackupStatusFailed,
	}
	expected := []string{"pending", "running", "complete", "failed"}

	for i, s := range statuses {
		if string(s) != expected[i] {
			t.Fatalf("expected status %q, got %q", expected[i], string(s))
		}
	}
}

func TestCompressionTypeConstants(t *testing.T) {
	types := []CompressionType{
		CompressionNone,
		CompressionZstd,
		CompressionSnappy,
	}
	expected := []string{"none", "zstd", "snappy"}

	for i, ct := range types {
		if string(ct) != expected[i] {
			t.Fatalf("expected compression %q, got %q", expected[i], string(ct))
		}
	}
}

func TestEncryptionTypeConstants(t *testing.T) {
	types := []EncryptionType{
		EncryptionNone,
		EncryptionAES256,
	}
	expected := []string{"none", "aes256"}

	for i, et := range types {
		if string(et) != expected[i] {
			t.Fatalf("expected encryption %q, got %q", expected[i], string(et))
		}
	}
}

// ---------------------------------------------------------------------------
// ListBackupsResponse serialization test
// ---------------------------------------------------------------------------

func TestListBackupsResponse_JSON_Roundtrip(t *testing.T) {
	resp := ListBackupsResponse{
		Backups: []BackupInfo{
			{ID: "a", Status: BackupStatusComplete},
			{ID: "b", Status: BackupStatusFailed},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal ListBackupsResponse: %v", err)
	}

	var decoded ListBackupsResponse
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal ListBackupsResponse: %v", err)
	}

	if len(decoded.Backups) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(decoded.Backups))
	}
	if decoded.Backups[0].ID != "a" {
		t.Fatalf("expected first backup ID a, got %q", decoded.Backups[0].ID)
	}
}
