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

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// executeBackupCmd constructs an embedded SDK client with the given backup
// service, installs it as the package-level client, and executes the backup
// command tree with the given args. It returns captured stdout and any error.
func executeBackupCmd(t *testing.T, backupSvc service.BackupServicer, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithBackupService(backupSvc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}

	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = "table"
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newBackupCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// executeBackupCmdCapturingStderr behaves like executeBackupCmd but
// captures the command's error stream (where warnPlaintextBackupCreate
// writes) instead of relaying it to the real process stderr, so a test can
// assert on it.
func executeBackupCmdCapturingStderr(t *testing.T, backupSvc service.BackupServicer, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	tr, trErr := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithBackupService(backupSvc))
	if trErr != nil {
		t.Fatalf("embedded.New: %v", trErr)
	}

	sdkClient, clientErr := sdk.NewClient(tr)
	if clientErr != nil {
		t.Fatalf("sdk.NewClient: %v", clientErr)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = "table"
	var outBuf, errBuf bytes.Buffer
	outputWriter = &outBuf

	cmd := newBackupCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return outBuf.String(), errBuf.String(), execErr
}

// ---------------------------------------------------------------------------
// backup create tests
// ---------------------------------------------------------------------------

func TestBackupCreateCmd_Success(t *testing.T) {
	svc := &mockBackupService{}
	out, err := executeBackupCmd(t, svc, "create", "--destination", "/backups/test")
	if err != nil {
		t.Fatalf("backup create: %v", err)
	}
	if !strings.Contains(out, "bkp-001") {
		t.Fatalf("expected backup ID in output, got: %s", out)
	}
}

func TestBackupCreateCmd_MissingDestination(t *testing.T) {
	svc := &mockBackupService{}
	_, err := executeBackupCmd(t, svc, "create")
	if err == nil {
		t.Fatal("expected error when --destination is missing")
	}
}

func TestBackupCreateCmd_InvalidCompression(t *testing.T) {
	svc := &mockBackupService{}
	_, err := executeBackupCmd(t, svc, "create", "--destination", "/backups/test", "--compression", "lz4")
	if err == nil {
		t.Fatal("expected error for invalid compression type")
	}
	var argErr *InvalidArgError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected InvalidArgError, got %T: %v", err, err)
	}
}

func TestBackupCreateCmd_InvalidEncryption(t *testing.T) {
	svc := &mockBackupService{}
	_, err := executeBackupCmd(t, svc, "create", "--destination", "/backups/test", "--encryption", "rsa")
	if err == nil {
		t.Fatal("expected error for invalid encryption type")
	}
	var argErr *InvalidArgError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected InvalidArgError, got %T: %v", err, err)
	}
}

func TestBackupCreateCmd_WithCompression(t *testing.T) {
	svc := &mockBackupService{}
	out, err := executeBackupCmd(t, svc, "create", "--destination", "/backups/test", "--compression", "zstd")
	if err != nil {
		t.Fatalf("backup create --compression zstd: %v", err)
	}
	if out == "" {
		t.Fatal("expected output")
	}
}

func TestBackupCreateCmd_WithEncryption(t *testing.T) {
	svc := &mockBackupService{}
	out, err := executeBackupCmd(t, svc, "create", "--destination", "/backups/test",
		"--encryption", "aes256", "--encryption-key", "supersecret")
	if err != nil {
		t.Fatalf("backup create --encryption aes256: %v", err)
	}
	if out == "" {
		t.Fatal("expected output")
	}
}

// TestBackupCreateCmd_UnsetEncryptionPassesThroughEmpty confirms the CLI
// does not coerce an unset --encryption flag to "none": the request the
// service sees carries an empty Encryption, letting the server apply its
// own default (aes256, requiring a key) rather than the CLI silently
// picking plaintext.
func TestBackupCreateCmd_UnsetEncryptionPassesThroughEmpty(t *testing.T) {
	var captured service.BackupOptions
	svc := &mockBackupService{
		createFn: func(_ context.Context, opts service.BackupOptions) (*service.BackupInfo, error) {
			captured = opts
			return &service.BackupInfo{ID: "bkp-001", Status: service.BackupStatusComplete}, nil
		},
	}
	_, err := executeBackupCmd(t, svc, "create", "--destination", "/backups/test")
	if err != nil {
		t.Fatalf("backup create: %v", err)
	}
	if captured.Encryption != "" {
		t.Fatalf("expected empty Encryption to pass through when --encryption is unset, got %q", captured.Encryption)
	}
}

// TestBackupCreateCmd_ExplicitNoneWarns confirms --encryption none prints a
// plaintext warning to the command's error stream before creating the
// backup.
func TestBackupCreateCmd_ExplicitNoneWarns(t *testing.T) {
	svc := &mockBackupService{}
	out, errOut, err := executeBackupCmdCapturingStderr(t, svc, "create",
		"--destination", "/backups/plain", "--encryption", "none")
	if err != nil {
		t.Fatalf("backup create --encryption none: %v", err)
	}
	if out == "" {
		t.Fatal("expected output")
	}
	if !strings.Contains(errOut, "plaintext") {
		t.Fatalf("expected a plaintext warning on stderr, got: %q", errOut)
	}
	if !strings.Contains(errOut, "/backups/plain") {
		t.Fatalf("expected the warning to name the destination, got: %q", errOut)
	}
}

// TestBackupCreateCmd_AES256DoesNotWarn confirms an encrypted backup
// request (the default) prints nothing to the error stream.
func TestBackupCreateCmd_AES256DoesNotWarn(t *testing.T) {
	svc := &mockBackupService{}
	_, errOut, err := executeBackupCmdCapturingStderr(t, svc, "create",
		"--destination", "/backups/encrypted", "--encryption", "aes256", "--encryption-key", "supersecret")
	if err != nil {
		t.Fatalf("backup create --encryption aes256: %v", err)
	}
	if errOut != "" {
		t.Fatalf("expected no warning for an encrypted backup, got: %q", errOut)
	}
}

func TestBackupCreateCmd_OutputJSON(t *testing.T) {
	svc := &mockBackupService{}

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithBackupService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = "json"
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newBackupCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"create", "--destination", "/backups/test"})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("backup create --output json: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"id"`) {
		t.Fatalf("expected JSON output with id field, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// backup list tests
// ---------------------------------------------------------------------------

func TestBackupListCmd_Success(t *testing.T) {
	svc := &mockBackupService{}
	out, err := executeBackupCmd(t, svc, "list")
	if err != nil {
		t.Fatalf("backup list: %v", err)
	}
	if !strings.Contains(out, "bkp-001") {
		t.Fatalf("expected bkp-001 in output, got: %s", out)
	}
}

func TestBackupListCmd_Empty(t *testing.T) {
	svc := &mockBackupService{
		listFn: func(_ context.Context) ([]service.BackupInfo, error) {
			return nil, nil
		},
	}
	out, err := executeBackupCmd(t, svc, "list")
	if err != nil {
		t.Fatalf("backup list empty: %v", err)
	}
	if !strings.Contains(out, "Total: 0") {
		t.Fatalf("expected 'Total: 0' in output, got: %s", out)
	}
}

func TestBackupListCmd_ServiceError(t *testing.T) {
	svc := &mockBackupService{
		listFn: func(_ context.Context) ([]service.BackupInfo, error) {
			return nil, errors.New("storage unavailable")
		},
	}
	_, err := executeBackupCmd(t, svc, "list")
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// backup get tests
// ---------------------------------------------------------------------------

func TestBackupGetCmd_Success(t *testing.T) {
	svc := &mockBackupService{}
	out, err := executeBackupCmd(t, svc, "get", "bkp-001")
	if err != nil {
		t.Fatalf("backup get: %v", err)
	}
	if !strings.Contains(out, "bkp-001") {
		t.Fatalf("expected bkp-001 in output, got: %s", out)
	}
}

func TestBackupGetCmd_NotFound(t *testing.T) {
	svc := &mockBackupService{}
	_, err := executeBackupCmd(t, svc, "get", "notfound")
	if err == nil {
		t.Fatal("expected error for unknown backup")
	}
}

func TestBackupGetCmd_MissingArg(t *testing.T) {
	svc := &mockBackupService{}
	_, err := executeBackupCmd(t, svc, "get")
	if err == nil {
		t.Fatal("expected error when ID argument is missing")
	}
}

// ---------------------------------------------------------------------------
// backup delete tests
// ---------------------------------------------------------------------------

func TestBackupDeleteCmd_Success(t *testing.T) {
	svc := &mockBackupService{}
	out, err := executeBackupCmd(t, svc, "delete", "bkp-001")
	if err != nil {
		t.Fatalf("backup delete: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK in output, got: %s", out)
	}
}

func TestBackupDeleteCmd_NotFound(t *testing.T) {
	svc := &mockBackupService{}
	_, err := executeBackupCmd(t, svc, "delete", "notfound")
	if err == nil {
		t.Fatal("expected error for unknown backup")
	}
}

func TestBackupDeleteCmd_MissingArg(t *testing.T) {
	svc := &mockBackupService{}
	_, err := executeBackupCmd(t, svc, "delete")
	if err == nil {
		t.Fatal("expected error when ID argument is missing")
	}
}

// ---------------------------------------------------------------------------
// backup restore tests
// ---------------------------------------------------------------------------

func TestBackupRestoreCmd_Success(t *testing.T) {
	svc := &mockBackupService{}
	out, err := executeBackupCmd(t, svc, "restore", "--id", "bkp-001")
	if err != nil {
		t.Fatalf("backup restore: %v", err)
	}
	if !strings.Contains(out, "bkp-001") {
		t.Fatalf("expected bkp-001 in output, got: %s", out)
	}
}

func TestBackupRestoreCmd_WithValidate(t *testing.T) {
	var capturedOpts service.RestoreOptions
	svc := &mockBackupService{
		restoreFn: func(_ context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
			capturedOpts = opts
			return &service.RestoreResult{
				BackupID:       opts.BackupID,
				ShardsRestored: 2,
				Duration:       50 * time.Millisecond,
			}, nil
		},
	}
	_, err := executeBackupCmd(t, svc, "restore", "--id", "bkp-001", "--validate")
	if err != nil {
		t.Fatalf("backup restore --validate: %v", err)
	}
	if !capturedOpts.ValidateFirst {
		t.Fatal("expected ValidateFirst to be true")
	}
}

func TestBackupRestoreCmd_WithShards(t *testing.T) {
	var capturedOpts service.RestoreOptions
	svc := &mockBackupService{
		restoreFn: func(_ context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
			capturedOpts = opts
			return &service.RestoreResult{
				BackupID:       opts.BackupID,
				ShardsRestored: 1,
				Duration:       50 * time.Millisecond,
			}, nil
		},
	}
	_, err := executeBackupCmd(t, svc, "restore", "--id", "bkp-001", "--shards", "1,2")
	if err != nil {
		t.Fatalf("backup restore --shards: %v", err)
	}
	if len(capturedOpts.TargetShards) != 2 {
		t.Fatalf("expected 2 target shards, got %d", len(capturedOpts.TargetShards))
	}
}

func TestBackupRestoreCmd_InvalidShardID(t *testing.T) {
	svc := &mockBackupService{}
	_, err := executeBackupCmd(t, svc, "restore", "--id", "bkp-001", "--shards", "abc")
	if err == nil {
		t.Fatal("expected error for invalid shard ID")
	}
	var argErr *InvalidArgError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected InvalidArgError, got %T: %v", err, err)
	}
}

func TestBackupRestoreCmd_PartialFailure(t *testing.T) {
	svc := &mockBackupService{
		restoreFn: func(_ context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
			return &service.RestoreResult{
				BackupID:       opts.BackupID,
				ShardsRestored: 1,
				Duration:       50 * time.Millisecond,
				Errors: []service.ShardError{
					{ShardID: 2, Message: "shard unreachable"},
				},
			}, nil
		},
	}
	out, err := executeBackupCmd(t, svc, "restore", "--id", "bkp-001")
	if err == nil {
		t.Fatal("expected a non-nil error for a partial restore failure")
	}
	var partialErr *RestorePartialFailureError
	if !errors.As(err, &partialErr) {
		t.Fatalf("expected *RestorePartialFailureError, got %T: %v", err, err)
	}
	if partialErr.FailedCount != 1 {
		t.Errorf("expected FailedCount 1, got %d", partialErr.FailedCount)
	}
	// The per-shard detail must still be printed even though the command
	// now returns a non-nil error.
	if !strings.Contains(out, "shard unreachable") {
		t.Fatalf("expected shard error detail in output, got: %s", out)
	}
}

func TestBackupRestoreCmd_MissingID(t *testing.T) {
	svc := &mockBackupService{}
	_, err := executeBackupCmd(t, svc, "restore")
	if err == nil {
		t.Fatal("expected error when --id is missing")
	}
}

// ---------------------------------------------------------------------------
// backup verify tests
// ---------------------------------------------------------------------------

func TestBackupVerifyCmd_Success(t *testing.T) {
	svc := &mockBackupService{}
	out, err := executeBackupCmd(t, svc, "verify", "bkp-001")
	if err != nil {
		t.Fatalf("backup verify: %v", err)
	}
	if !strings.Contains(out, "true") {
		t.Fatalf("expected valid=true in output, got: %s", out)
	}
}

func TestBackupVerifyCmd_Invalid(t *testing.T) {
	svc := &mockBackupService{
		verifyFn: func(_ context.Context, _ string) (*service.VerifyResult, error) {
			return &service.VerifyResult{
				Valid:            false,
				ChecksumMatch:    false,
				ExpectedChecksum: "abc",
				ActualChecksum:   "xyz",
				Errors:           []string{"checksum mismatch"},
			}, nil
		},
	}
	out, err := executeBackupCmd(t, svc, "verify", "bkp-corrupt")
	if err != nil {
		t.Fatalf("backup verify invalid: %v", err)
	}
	if !strings.Contains(out, "false") {
		t.Fatalf("expected valid=false in output, got: %s", out)
	}
}

func TestBackupVerifyCmd_MissingArg(t *testing.T) {
	svc := &mockBackupService{}
	_, err := executeBackupCmd(t, svc, "verify")
	if err == nil {
		t.Fatal("expected error when ID argument is missing")
	}
}

// ---------------------------------------------------------------------------
// Helper function tests
// ---------------------------------------------------------------------------

func TestParseCompressionType_Valid(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"none", "none"},
		{"zstd", "zstd"},
		{"snappy", "snappy"},
		{"ZSTD", "zstd"},
	}
	for _, tc := range tests {
		got, err := parseCompressionType(tc.input)
		if err != nil {
			t.Errorf("parseCompressionType(%q): %v", tc.input, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("parseCompressionType(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestParseCompressionType_Invalid(t *testing.T) {
	_, err := parseCompressionType("lz4")
	if err == nil {
		t.Fatal("expected error for unknown compression type")
	}
	var argErr *InvalidArgError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected InvalidArgError, got %T", err)
	}
}

func TestParseEncryptionType_Valid(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"none", "none"},
		{"aes256", "aes256"},
		{"AES256", "aes256"},
	}
	for _, tc := range tests {
		got, err := parseEncryptionType(tc.input)
		if err != nil {
			t.Errorf("parseEncryptionType(%q): %v", tc.input, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("parseEncryptionType(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestParseEncryptionType_UnsetPassesThrough confirms that an unset
// --encryption flag (empty string, the flag's own default) is not coerced
// to "none" by the CLI: it passes through empty so the server applies its
// own default (aes256, requiring a key) instead of the CLI silently
// choosing plaintext.
func TestParseEncryptionType_UnsetPassesThrough(t *testing.T) {
	got, err := parseEncryptionType("")
	if err != nil {
		t.Fatalf("parseEncryptionType(\"\"): %v", err)
	}
	if got != "" {
		t.Fatalf("parseEncryptionType(\"\") = %q, want empty", got)
	}
}

func TestParseEncryptionType_Invalid(t *testing.T) {
	_, err := parseEncryptionType("rsa4096")
	if err == nil {
		t.Fatal("expected error for unknown encryption type")
	}
}

func TestParseShardIDs_Valid(t *testing.T) {
	ids, err := parseShardIDs("1,2,3")
	if err != nil {
		t.Fatalf("parseShardIDs: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 shard IDs, got %d", len(ids))
	}
}

func TestParseShardIDs_Invalid(t *testing.T) {
	_, err := parseShardIDs("1,abc,3")
	if err == nil {
		t.Fatal("expected error for non-numeric shard ID")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{500, "500 B"},
		{1024, "1.00 KB"},
		{1024 * 1024, "1.00 MB"},
		{1024 * 1024 * 1024, "1.00 GB"},
	}
	for _, tc := range tests {
		got := formatBytes(tc.input)
		if got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
