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

import "testing"

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
