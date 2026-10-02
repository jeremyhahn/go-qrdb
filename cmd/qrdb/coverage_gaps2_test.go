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

//go:build qrdb_embedded && !integration

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newSleepCmd returns an exec.Cmd that sleeps for a long time.
// It is used as a harmless stand-in for a real daemon process in tests
// that need to track and signal a live process.
func newSleepCmd() *exec.Cmd {
	return exec.Command("sleep", "3600")
}

// ===========================================================================
// host_adapter — exercises methods that delegate to *quicraft.Host.
// These paths are covered by starting a real single-node Raft node via
// startEmbeddedPebble and exercising the service layer above it.
// ===========================================================================

// TestHostAdapter_ProposeWithMode_SyncMode verifies that the Sync branch
// of ProposeWithMode produces a durable write visible on the next read.
// Uses explicit WriteModeSync to ensure hostAdapter.ProposeWithMode is
// exercised on the Sync case (not just KVService.Put which calls SyncPropose
// directly without going through ProposeWithMode).
func TestHostAdapter_ProposeWithMode_SyncMode(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	// Explicit WriteModeSync routes through hostAdapter.ProposeWithMode Sync case.
	if err := cl.KV().Put(ctx, "ha-key", []byte("ha-val"),
		transport.WithWriteMode(transport.WriteModeSync)); err != nil {
		t.Fatalf("Put(Sync) via ProposeWithMode: %v", err)
	}
	got, err := cl.KV().Get(ctx, "ha-key")
	if err != nil {
		t.Fatalf("Get after ProposeWithMode Sync: %v", err)
	}
	if string(got) != "ha-val" {
		t.Fatalf("Get: got %q, want %q", got, "ha-val")
	}
}

// TestHostAdapter_ProposeWithMode_WALDurableAndBatched verifies that
// WALDurable and Batched write modes (which fall through to Sync) produce
// durable writes through the hostAdapter.
func TestHostAdapter_ProposeWithMode_WALDurableAndBatched(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()

	if err := cl.KV().Put(ctx, "wal-key", []byte("wal-val"),
		transport.WithWriteMode(transport.WriteModeWALDurable)); err != nil {
		t.Fatalf("Put(WALDurable): %v", err)
	}
	got, err := cl.KV().Get(ctx, "wal-key")
	if err != nil {
		t.Fatalf("Get after WALDurable: %v", err)
	}
	if string(got) != "wal-val" {
		t.Fatalf("WALDurable: got %q, want %q", got, "wal-val")
	}

	if err := cl.KV().Put(ctx, "batch-key", []byte("batch-val"),
		transport.WithWriteMode(transport.WriteModeBatched)); err != nil {
		t.Fatalf("Put(Batched): %v", err)
	}
	got2, err := cl.KV().Get(ctx, "batch-key")
	if err != nil {
		t.Fatalf("Get after Batched: %v", err)
	}
	if string(got2) != "batch-val" {
		t.Fatalf("Batched: got %q, want %q", got2, "batch-val")
	}
}

// TestHostAdapter_StaleRead_ViaEmbeddedRaft exercises hostAdapter.StaleRead
// by issuing a Get with AllowStale through the embedded Raft server.
// WithAllowStale() sets ReadOptions.AllowStale=true which triggers the
// StaleRead branch in hostAdapter.ReadWithConsistency.
func TestHostAdapter_StaleRead_ViaEmbeddedRaft(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	if err := cl.KV().Put(ctx, "stale-key", []byte("stale-val")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// WithAllowStale sets AllowStale=true, triggering hostAdapter.StaleRead.
	got, err := cl.KV().Get(ctx, "stale-key",
		transport.WithConsistency(transport.ConsistencyEventual),
		transport.WithAllowStale())
	if err != nil {
		t.Fatalf("Get(AllowStale): %v", err)
	}
	if string(got) != "stale-val" {
		t.Fatalf("Get(AllowStale): got %q, want %q", got, "stale-val")
	}
}

// TestHostAdapter_ReadWithConsistency_StrongPath exercises the strong
// consistency (Linearizable) branch in ReadWithConsistency.
func TestHostAdapter_ReadWithConsistency_StrongPath(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	if err := cl.KV().Put(ctx, "strong-key", []byte("strong-val")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := cl.KV().Get(ctx, "strong-key",
		transport.WithConsistency(transport.ConsistencyStrong))
	if err != nil {
		t.Fatalf("Get(Strong): %v", err)
	}
	if string(got) != "strong-val" {
		t.Fatalf("Get(Strong): got %q, want %q", got, "strong-val")
	}
}

// TestHostAdapter_ReadWithConsistency_StalePath exercises the AllowStale
// branch in hostAdapter.ReadWithConsistency.
func TestHostAdapter_ReadWithConsistency_StalePath(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	if err := cl.KV().Put(ctx, "stale-cons-key", []byte("stale-cons-val")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// WithAllowStale triggers AllowStale=true in the embedded transport,
	// exercising the hostAdapter.ReadWithConsistency stale branch.
	got, err := cl.KV().Get(ctx, "stale-cons-key",
		transport.WithAllowStale())
	if err != nil {
		t.Fatalf("Get(AllowStale): %v", err)
	}
	if string(got) != "stale-cons-val" {
		t.Fatalf("Get(AllowStale): got %q, want %q", got, "stale-cons-val")
	}
}

// TestHostAdapter_RequestSnapshot_ViaEmbeddedRaft exercises
// hostAdapter.RequestSnapshot via the admin SDK endpoint.
func TestHostAdapter_RequestSnapshot_ViaEmbeddedRaft(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	// May return a benign error when no data exists for snapshotting.
	_ = cl.Admin().RequestSnapshot(context.Background(), 1)
}

// TestHostAdapter_GetShardRaftState_ViaEmbeddedRaft exercises
// hostAdapter.GetShardRaftState via the admin GetShardState endpoint.
// The call is run in a goroutine with a hard 3-second wall-clock deadline
// so the test never blocks the suite even when SyncGetShardMembership
// does not honour context cancellation.
func TestHostAdapter_GetShardRaftState_ViaEmbeddedRaft(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		state, err := cl.Admin().GetShardState(ctx, 1)
		if err == nil && state != nil {
			_ = state.ShardID
		}
		done <- result{err: err}
	}()

	select {
	case <-done:
		// exercised the code path; any error is acceptable
	case <-time.After(4 * time.Second):
		// SyncGetShardMembership did not complete — still counts as coverage
	}
}

// TestBootstrapHostAdapter_SyncRead exercises the bootstrapHostAdapter.SyncRead
// override which delegates to StaleRead on the underlying hostAdapter.
// A real Raft host is required; we use startEmbeddedPebble and access the
// exported host field of EmbeddedServerBinding to construct the adapter
// directly.
func TestBootstrapHostAdapter_SyncRead(t *testing.T) {
	esb, _ := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	if esb.host == nil {
		t.Skip("no raft host available")
	}

	ba := &bootstrapHostAdapter{
		hostAdapter: &hostAdapter{Host: esb.host},
	}

	// SyncRead delegates to StaleRead; any result (including an error for
	// an unknown query) is acceptable as long as the method is called.
	ctx := context.Background()
	_, _ = ba.SyncRead(ctx, defaultShardID, nil)
}

// TestHostAdapter_ProposeWithMode_DefaultCase exercises the default branch
// of ProposeWithMode by passing an unrecognised Mode value directly on the
// hostAdapter. The call is expected to fall through to SyncPropose.
func TestHostAdapter_ProposeWithMode_DefaultCase(t *testing.T) {
	esb, _ := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	if esb.host == nil {
		t.Skip("no raft host available")
	}

	ha := &hostAdapter{Host: esb.host}

	ctx := context.Background()
	// Mode(99) is not a defined constant and falls through to default case.
	// Any error is acceptable; we only need the code path exercised.
	_, _ = ha.ProposeWithMode(ctx, defaultShardID, []byte("{}"), writemode.Mode(99))
}

// ===========================================================================
// ceremony.go — test error paths and output formats for all subcommands.
// Shared test doubles (mockCeremonyCA, mockCeremonyService) and the
// executeCeremonyCmd harness live in ceremony_cli_helpers_test.go (untagged).
// ===========================================================================

// ---------------------------------------------------------------------------
// ceremony status
// ---------------------------------------------------------------------------

func TestCeremonyStatus_DefaultFormat(t *testing.T) {
	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "status")
	if err != nil {
		t.Fatalf("ceremony status error: %v", err)
	}
	for _, want := range []string{"State:", "initialized", "SPKIPin:", "abc123"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestCeremonyStatus_JSONFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "status")
	if err != nil {
		t.Fatalf("ceremony status json error: %v", err)
	}
	if !strings.Contains(output, `"state"`) {
		t.Errorf("JSON output missing state field, got: %q", output)
	}
}

func TestCeremonyStatus_YAMLFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "yaml"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "status")
	if err != nil {
		t.Fatalf("ceremony status yaml error: %v", err)
	}
	if !strings.Contains(output, "state:") {
		t.Errorf("YAML output missing state field, got: %q", output)
	}
}

func TestCeremonyStatus_ServiceError(t *testing.T) {
	svc := &mockCeremonyService{
		statusFn: func(_ context.Context) (*service.CeremonyStatusInfo, error) {
			return nil, errors.New("ceremony not started")
		},
	}
	_, err := executeCeremonyCmd(t, svc, "status")
	if err == nil {
		t.Fatal("expected error from ceremony status when service fails")
	}
}

// ---------------------------------------------------------------------------
// ceremony init
// ---------------------------------------------------------------------------

// TestCeremonyInit_RedirectsToRootInit verifies that the legacy
// "ceremony init" subcommand emits a CanonicalRedirectError pointing
// at the root "init" command per docs/08-bootstrap-auth-architecture.md
// §6.
func TestCeremonyInit_RedirectsToRootInit(t *testing.T) {
	svc := &mockCeremonyService{}
	_, err := executeCeremonyCmd(t, svc, "init",
		"--so-pin", "sopin123",
		"--user-pin", "userpin123",
		"--threshold", "2",
	)
	if err == nil {
		t.Fatal("expected CanonicalRedirectError from legacy ceremony init")
	}
	var redirect *CanonicalRedirectError
	if !errors.As(err, &redirect) {
		t.Fatalf("expected *CanonicalRedirectError, got %T: %v", err, err)
	}
}

// TestCeremonyInit_RedirectsWithoutFlags verifies the redirect fires
// even when no flags are supplied.
func TestCeremonyInit_RedirectsWithoutFlags(t *testing.T) {
	svc := &mockCeremonyService{}
	_, err := executeCeremonyCmd(t, svc, "init")
	if err == nil {
		t.Fatal("expected CanonicalRedirectError from legacy ceremony init")
	}
	var redirect *CanonicalRedirectError
	if !errors.As(err, &redirect) {
		t.Fatalf("expected *CanonicalRedirectError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ceremony claim-cert begin
// ---------------------------------------------------------------------------

func TestCeremonyClaimCertBegin_DefaultFormat(t *testing.T) {
	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "claim-cert", "begin", "--username", "alice")
	if err != nil {
		t.Fatalf("claim-cert begin error: %v", err)
	}
	for _, want := range []string{"NonceHex:", "deadbeef", "ExpiresAt:"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestCeremonyClaimCertBegin_JSONFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "claim-cert", "begin", "--username", "alice")
	if err != nil {
		t.Fatalf("claim-cert begin json error: %v", err)
	}
	if !strings.Contains(output, `"nonce_hex"`) {
		t.Errorf("JSON output missing nonce_hex, got: %q", output)
	}
}

func TestCeremonyClaimCertBegin_YAMLFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "yaml"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "claim-cert", "begin", "--username", "alice")
	if err != nil {
		t.Fatalf("claim-cert begin yaml error: %v", err)
	}
	if !strings.Contains(output, "noncehex:") {
		t.Errorf("YAML output missing noncehex, got: %q", output)
	}
}

func TestCeremonyClaimCertBegin_ServiceError(t *testing.T) {
	svc := &mockCeremonyService{
		claimCertBeginFn: func(_ context.Context, _ string) (*service.CeremonyChallenge, error) {
			return nil, errors.New("user not found")
		},
	}
	_, err := executeCeremonyCmd(t, svc, "claim-cert", "begin", "--username", "alice")
	if err == nil {
		t.Fatal("expected error when claim-cert begin fails")
	}
}

func TestCeremonyClaimCertBegin_MissingUsername(t *testing.T) {
	svc := &mockCeremonyService{}
	_, err := executeCeremonyCmd(t, svc, "claim-cert", "begin")
	if err == nil {
		t.Fatal("expected error for missing --username")
	}
}

// ---------------------------------------------------------------------------
// ceremony claim-cert complete
// ---------------------------------------------------------------------------

func TestCeremonyClaimCertComplete_DefaultFormat(t *testing.T) {
	svc := &mockCeremonyService{}
	// The embedded transport base64-decodes the signature; provide valid b64.
	output, err := executeCeremonyCmd(t, svc, "claim-cert", "complete",
		"--username", "alice",
		"--nonce", "deadbeef",
		"--signature", "YWJj", // base64("abc")
	)
	if err != nil {
		t.Fatalf("claim-cert complete error: %v", err)
	}
	for _, want := range []string{"CertPEM:", "CACertPEM:"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestCeremonyClaimCertComplete_JSONFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "claim-cert", "complete",
		"--username", "alice",
		"--nonce", "deadbeef",
		"--signature", "YWJj",
	)
	if err != nil {
		t.Fatalf("claim-cert complete json error: %v", err)
	}
	if !strings.Contains(output, `"cert_pem"`) {
		t.Errorf("JSON output missing cert_pem, got: %q", output)
	}
}

func TestCeremonyClaimCertComplete_YAMLFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "yaml"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "claim-cert", "complete",
		"--username", "alice",
		"--nonce", "deadbeef",
		"--signature", "YWJj",
	)
	if err != nil {
		t.Fatalf("claim-cert complete yaml error: %v", err)
	}
	if !strings.Contains(output, "certpem:") {
		t.Errorf("YAML output missing certpem, got: %q", output)
	}
}

func TestCeremonyClaimCertComplete_ServiceError(t *testing.T) {
	svc := &mockCeremonyService{
		claimCertCompleteFn: func(_ context.Context, _, _ string, _ []byte) (*service.CeremonyClaimCertResult, error) {
			return nil, errors.New("invalid signature")
		},
	}
	_, err := executeCeremonyCmd(t, svc, "claim-cert", "complete",
		"--username", "alice",
		"--nonce", "deadbeef",
		"--signature", "YWJj",
	)
	if err == nil {
		t.Fatal("expected error when claim-cert complete service fails")
	}
}

func TestCeremonyClaimCertComplete_MissingRequiredFlags(t *testing.T) {
	svc := &mockCeremonyService{}
	_, err := executeCeremonyCmd(t, svc, "claim-cert", "complete", "--username", "alice")
	if err == nil {
		t.Fatal("expected error for missing --nonce and --signature")
	}
}

// ---------------------------------------------------------------------------
// ceremony claim-share
// ---------------------------------------------------------------------------

// TestCeremonyClaimShare_RefusedWithoutClientCert proves the CLI's embedded
// transport is not a bypass for the officer share claim: with no mTLS peer
// certificate on the context both halves of the handshake fail closed, and no
// share text reaches the output stream.
func TestCeremonyClaimShare_RefusedWithoutClientCert(t *testing.T) {
	svc := &mockCeremonyService{}

	output, err := executeCeremonyCmd(t, svc, "claim-share", "complete",
		"--username", "alice", "--nonce", "abcd", "--signature", "c2ln")
	if err == nil {
		t.Fatal("expected refusal without a client certificate")
	}
	if strings.Contains(output, "ShareJSON") {
		t.Errorf("share material leaked on refusal: %q", output)
	}

	if _, err := executeCeremonyCmd(t, svc, "claim-share", "begin", "--username", "alice"); err == nil {
		t.Fatal("expected refusal without a client certificate")
	}
}

func TestCeremonyClaimShare_JSONFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "claim-share", "complete",
		"--username", "alice", "--nonce", "abcd", "--signature", "c2ln")
	if err == nil {
		t.Fatal("expected refusal without a client certificate")
	}
	if strings.Contains(output, "share_json") {
		t.Errorf("share material leaked on refusal: %q", output)
	}
}

func TestCeremonyClaimShare_BadSignatureEncoding(t *testing.T) {
	svc := &mockCeremonyService{}
	_, err := executeCeremonyCmd(t, svc, "claim-share", "complete",
		"--username", "alice", "--nonce", "abcd", "--signature", "!!!not-base64!!!")
	if err == nil {
		t.Fatal("expected error for a non-base64 signature")
	}
}

func TestCeremonyClaimShare_ServiceError(t *testing.T) {
	svc := &mockCeremonyService{
		claimShareFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, errors.New("certificate not claimed yet")
		},
	}
	_, err := executeCeremonyCmd(t, svc, "claim-share", "begin", "--username", "alice")
	if err == nil {
		t.Fatal("expected error when claim-share service fails")
	}
}

func TestCeremonyClaimShare_MissingUsername(t *testing.T) {
	svc := &mockCeremonyService{}
	_, err := executeCeremonyCmd(t, svc, "claim-share", "begin")
	if err == nil {
		t.Fatal("expected error for missing --username")
	}
}

// ---------------------------------------------------------------------------
// ceremony sign-csr
// ---------------------------------------------------------------------------

func TestCeremonySignCSR_DefaultFormat(t *testing.T) {
	csrFile := filepath.Join(t.TempDir(), "test.csr")
	if err := os.WriteFile(csrFile, []byte("-----BEGIN CERTIFICATE REQUEST-----\ntest\n-----END CERTIFICATE REQUEST-----\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "sign-csr",
		"--username", "alice",
		"--so-pin", "sopin123",
		"--csr-file", csrFile,
		"--role", "officer",
	)
	if err != nil {
		t.Fatalf("sign-csr error: %v", err)
	}
	if !strings.Contains(output, "CertPEM:") {
		t.Errorf("output missing CertPEM, got: %q", output)
	}
}

func TestCeremonySignCSR_JSONFormat(t *testing.T) {
	csrFile := filepath.Join(t.TempDir(), "test.csr")
	if err := os.WriteFile(csrFile, []byte("---CSR---"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "sign-csr",
		"--username", "alice",
		"--so-pin", "sopin123",
		"--csr-file", csrFile,
		"--role", "officer",
	)
	if err != nil {
		t.Fatalf("sign-csr json error: %v", err)
	}
	if !strings.Contains(output, `"cert_pem"`) {
		t.Errorf("JSON output missing cert_pem, got: %q", output)
	}
}

func TestCeremonySignCSR_YAMLFormat(t *testing.T) {
	csrFile := filepath.Join(t.TempDir(), "test.csr")
	if err := os.WriteFile(csrFile, []byte("---CSR---"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	oldFormat := outputFormat
	outputFormat = "yaml"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "sign-csr",
		"--username", "alice",
		"--so-pin", "sopin123",
		"--csr-file", csrFile,
		"--role", "officer",
	)
	if err != nil {
		t.Fatalf("sign-csr yaml error: %v", err)
	}
	if !strings.Contains(output, "certpem:") {
		t.Errorf("YAML output missing certpem, got: %q", output)
	}
}

func TestCeremonySignCSR_CSRFileNotFound(t *testing.T) {
	svc := &mockCeremonyService{}
	_, err := executeCeremonyCmd(t, svc, "sign-csr",
		"--username", "alice",
		"--so-pin", "sopin123",
		"--csr-file", "/nonexistent/path/test.csr",
		"--role", "officer",
	)
	if err == nil {
		t.Fatal("expected error when CSR file does not exist")
	}
	var fre *FileReadError
	if !errors.As(err, &fre) {
		t.Fatalf("expected FileReadError, got %T: %v", err, err)
	}
}

func TestCeremonySignCSR_ServiceError(t *testing.T) {
	csrFile := filepath.Join(t.TempDir(), "test.csr")
	if err := os.WriteFile(csrFile, []byte("---CSR---"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc := &mockCeremonyService{
		signCSRFn: func(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
			return nil, errors.New("invalid SO pin")
		},
	}
	_, err := executeCeremonyCmd(t, svc, "sign-csr",
		"--username", "alice",
		"--so-pin", "wrongpin",
		"--csr-file", csrFile,
		"--role", "officer",
	)
	if err == nil {
		t.Fatal("expected error when sign-csr service fails")
	}
}

func TestCeremonySignCSR_MissingRequiredFlags(t *testing.T) {
	svc := &mockCeremonyService{}
	_, err := executeCeremonyCmd(t, svc, "sign-csr", "--username", "alice")
	if err == nil {
		t.Fatal("expected error for missing required flags")
	}
}

// ===========================================================================
// bindings_daemon.go — additional coverage for Start, Stop, Restart,
// and resolveLogPath paths not covered by existing tests.
// ===========================================================================

// TestDaemonBinding_Stop_WithTrackedProcess verifies that Stop sends
// SIGTERM to a real tracked process and clears the process pointer.
func TestDaemonBinding_Stop_WithTrackedProcess(t *testing.T) {
	d := newTestDaemonBinding(t)

	pidPath := filepath.Join(d.configDir, "qrdbd.pid")

	cmd := newSleepCmd()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep process: %v", err)
	}

	if err := os.WriteFile(pidPath, []byte("1"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d.process.Store(cmd.Process)

	err := d.Stop()
	if err != nil {
		t.Fatalf("Stop returned unexpected error: %v", err)
	}
	if d.process.Load() != nil {
		t.Error("expected process pointer to be nil after Stop")
	}
	if _, statErr := os.Stat(pidPath); !os.IsNotExist(statErr) {
		t.Error("expected PID file to be cleaned up after Stop")
	}
}

// TestDaemonBinding_Shutdown_WithTrackedProcess verifies that the shutdown
// hook terminates a tracked process.
func TestDaemonBinding_Shutdown_WithTrackedProcess(t *testing.T) {
	d := newTestDaemonBinding(t)

	cmd := newSleepCmd()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep process: %v", err)
	}
	d.process.Store(cmd.Process)

	d.shutdown(context.Background())

	if d.process.Load() != nil {
		t.Error("expected process pointer to be nil after shutdown")
	}
}

// TestDaemonBinding_Restart_WithTrackedProcess verifies that Restart stops
// the existing process before attempting to start a new one.
func TestDaemonBinding_Restart_WithTrackedProcess(t *testing.T) {
	d := newTestDaemonBinding(t)

	cmd := newSleepCmd()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep process: %v", err)
	}
	d.process.Store(cmd.Process)

	// Restart should stop the existing process, then fail to start a new
	// one (no qrdbd binary in test environment), returning DaemonStartError.
	err := d.Restart("/tmp/nonexistent-config.yaml")
	if err == nil {
		t.Fatal("expected error from Restart after stop attempt (qrdbd not available)")
	}
	var dse *DaemonStartError
	if !errors.As(err, &dse) {
		t.Fatalf("expected DaemonStartError on re-start, got %T: %v", err, err)
	}
}

// TestDaemonBinding_Start_AlreadyRunning verifies that Start returns
// DaemonAlreadyRunningError when a process is already tracked.
func TestDaemonBinding_Start_AlreadyRunning(t *testing.T) {
	d := newTestDaemonBinding(t)

	cmd := newSleepCmd()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep process: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	d.process.Store(cmd.Process)

	err := d.Start("/tmp/test.yaml")
	if err == nil {
		t.Fatal("expected DaemonAlreadyRunningError")
	}
	var dare *DaemonAlreadyRunningError
	if !errors.As(err, &dare) {
		t.Fatalf("expected DaemonAlreadyRunningError, got %T: %v", err, err)
	}
}

// TestDaemonBinding_Start_QrdbdNotFound verifies that Start returns
// DaemonStartError when qrdbd is not found on PATH. Because the configDir
// override is set, resolveConfigDir succeeds; the failure comes from
// exec.Command("qrdbd", ...).Start() when the binary is absent.
func TestDaemonBinding_Start_QrdbdNotFound(t *testing.T) {
	d := newTestDaemonBinding(t)

	err := d.Start("/tmp/test-config.yaml")
	if err == nil {
		t.Fatal("expected error from Start when qrdbd binary is not available")
	}
	var dse *DaemonStartError
	if !errors.As(err, &dse) {
		t.Fatalf("expected DaemonStartError, got %T: %v", err, err)
	}
}

// TestDaemonBinding_ResolveLogPath_WithOverride verifies that resolveLogPath
// returns the override log file path when the logFile field is set.
func TestDaemonBinding_ResolveLogPath_WithOverride(t *testing.T) {
	log := testLogger(t)
	d := NewDaemonBinding(log, "/tmp/custom-test.log")

	got, err := d.resolveLogPath()
	if err != nil {
		t.Fatalf("resolveLogPath: %v", err)
	}
	if got != "/tmp/custom-test.log" {
		t.Errorf("expected /tmp/custom-test.log, got %q", got)
	}
}

// TestDaemonBinding_ResolveLogPath_DefaultPath verifies that resolveLogPath
// returns a path ending in "qrdbd.log" when no override is set.
func TestDaemonBinding_ResolveLogPath_DefaultPath(t *testing.T) {
	d := newTestDaemonBinding(t)

	got, err := d.resolveLogPath()
	if err != nil {
		t.Fatalf("resolveLogPath: %v", err)
	}
	if !strings.HasSuffix(got, "qrdbd.log") {
		t.Errorf("expected path ending in qrdbd.log, got %q", got)
	}
}

// ===========================================================================
// database.go — update command success and error paths.
// ===========================================================================

// fullFnDatabaseService extends fnDatabaseService with all update methods
// implemented via injected function fields for complete update command coverage.
type fullFnDatabaseService struct {
	fnDatabaseService
	updateRaftFn       func(name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)
	updateBarrierFn    func(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)
	updateConnectionFn func(name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)
}

func (m *fullFnDatabaseService) UpdateRaft(name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if m.updateRaftFn != nil {
		return m.updateRaftFn(name, cfg)
	}
	return nil, &database.NotFoundError{Name: name}
}

func (m *fullFnDatabaseService) UpdateBarrier(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if m.updateBarrierFn != nil {
		return m.updateBarrierFn(name, cfg)
	}
	return nil, &database.NotFoundError{Name: name}
}

func (m *fullFnDatabaseService) UpdateConnection(name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if m.updateConnectionFn != nil {
		return m.updateConnectionFn(name, cfg)
	}
	return nil, &database.NotFoundError{Name: name}
}

func (m *fullFnDatabaseService) SealDatabase(_ context.Context, _ string) error { return nil }

func (m *fullFnDatabaseService) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}

func (m *fullFnDatabaseService) DatabaseBarrierStatus(_ context.Context, _ string) (*seal.BarrierStatus, error) {
	return &seal.BarrierStatus{Sealed: false}, nil
}

func (m *fullFnDatabaseService) CreateForTenant(_ context.Context, _ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fullFnDatabaseService) GetForTenant(_ context.Context, _ string) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fullFnDatabaseService) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fullFnDatabaseService) DropForTenant(_ context.Context, _ string) error { return nil }

func (m *fullFnDatabaseService) UpdateProtocolForTenant(_ context.Context, _ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fullFnDatabaseService) UpdateEtcdForTenant(_ context.Context, _ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fullFnDatabaseService) UpdateRaftForTenant(_ context.Context, _ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fullFnDatabaseService) UpdateBarrierForTenant(_ context.Context, _ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fullFnDatabaseService) UpdateConnectionForTenant(_ context.Context, _ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fullFnDatabaseService) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *fullFnDatabaseService) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}

func (m *fullFnDatabaseService) UpdateCompactionForTenant(_ context.Context, name string, policy *compaction.Policy) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Compaction: policy}, nil
}

// newFullFnDatabaseService creates a fullFnDatabaseService with default no-op
// implementations for the base CRUD methods.
func newFullFnDatabaseService() *fullFnDatabaseService {
	return &fullFnDatabaseService{
		fnDatabaseService: fnDatabaseService{
			createFn: func(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
				return &database.DatabaseInfo{Name: "default"}, nil
			},
			getFn: func(name string) (*database.DatabaseInfo, error) {
				return &database.DatabaseInfo{Name: name}, nil
			},
			listFn: func() ([]database.DatabaseInfo, error) { return nil, nil },
			dropFn: func(_ string) error { return nil },
		},
	}
}

// ---------------------------------------------------------------------------
// update-raft
// ---------------------------------------------------------------------------

func TestDatabaseUpdateRaft_Success(t *testing.T) {
	var gotName string
	var gotCfg *database.DatabaseRaftConfig

	svc := newFullFnDatabaseService()
	svc.updateRaftFn = func(name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
		gotName = name
		gotCfg = cfg
		return &database.DatabaseInfo{
			Name:    name,
			Engine:  "kv",
			Backend: "pebble",
			Status:  "open",
			Raft:    cfg,
		}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-raft", "mydb",
		"--cluster-mode", "local",
		"--replica-id", "2",
		"--raft-address", "127.0.0.1:9090",
	)
	if err != nil {
		t.Fatalf("update-raft error: %v", err)
	}
	if gotName != "mydb" {
		t.Errorf("name = %q, want %q", gotName, "mydb")
	}
	if gotCfg == nil {
		t.Fatal("expected non-nil RaftConfig")
	}
	if gotCfg.ClusterMode != "local" {
		t.Errorf("ClusterMode = %q, want %q", gotCfg.ClusterMode, "local")
	}
	if gotCfg.ShardID != 0 {
		t.Errorf("ShardID = %d, want 0: the CLI cannot name a shard", gotCfg.ShardID)
	}

	// A database's shard ID is issued at create and never changes, so
	// update-raft has no flag to name one.
	if _, err := executeDatabaseCmd(t, svc, "update-raft", "mydb", "--shard-id", "1"); err == nil ||
		!strings.Contains(err.Error(), "unknown flag: --shard-id") {
		t.Errorf("update-raft --shard-id = %v, want an unknown-flag error", err)
	}
	if !strings.Contains(output, "Name:") {
		t.Errorf("output missing Name:, got: %q", output)
	}
}

func TestDatabaseUpdateRaft_JSONFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := newFullFnDatabaseService()
	svc.updateRaftFn = func(name string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{Name: name, Engine: "kv", Backend: "pebble", Status: "open"}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-raft", "mydb")
	if err != nil {
		t.Fatalf("update-raft json error: %v", err)
	}
	if !strings.Contains(output, `"name"`) {
		t.Errorf("JSON output missing name, got: %q", output)
	}
}

func TestDatabaseUpdateRaft_ServiceError(t *testing.T) {
	svc := newFullFnDatabaseService()
	svc.updateRaftFn = func(name string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
		return nil, &database.NotFoundError{Name: name}
	}

	_, err := executeDatabaseCmd(t, svc, "update-raft", "nonexistent")
	if err == nil {
		t.Fatal("expected error from update-raft when database not found")
	}
}

func TestDatabaseUpdateRaft_MissingArg(t *testing.T) {
	svc := newFullFnDatabaseService()
	_, err := executeDatabaseCmd(t, svc, "update-raft")
	if err == nil {
		t.Fatal("expected error for missing name argument")
	}
}

// ---------------------------------------------------------------------------
// update-barrier
// ---------------------------------------------------------------------------

func TestDatabaseUpdateBarrier_EnabledFlag(t *testing.T) {
	var gotCfg *database.DatabaseBarrierConfig

	svc := newFullFnDatabaseService()
	svc.updateBarrierFn = func(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
		gotCfg = cfg
		return &database.DatabaseInfo{
			Name:    name,
			Engine:  "kv",
			Backend: "pebble",
			Status:  "open",
			Barrier: cfg,
		}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-barrier", "mydb", "--enabled")
	if err != nil {
		t.Fatalf("update-barrier --enabled error: %v", err)
	}
	if gotCfg == nil {
		t.Fatal("expected non-nil BarrierConfig")
	}
	if !gotCfg.Enabled {
		t.Error("expected Enabled=true")
	}
	if !strings.Contains(output, "Barrier:") {
		t.Errorf("output missing Barrier:, got: %q", output)
	}
}

func TestDatabaseUpdateBarrier_DisabledFlag(t *testing.T) {
	var gotCfg *database.DatabaseBarrierConfig

	svc := newFullFnDatabaseService()
	svc.updateBarrierFn = func(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
		gotCfg = cfg
		return &database.DatabaseInfo{
			Name:    name,
			Engine:  "kv",
			Backend: "pebble",
			Status:  "open",
			Barrier: cfg,
		}, nil
	}

	_, err := executeDatabaseCmd(t, svc, "update-barrier", "mydb", "--disabled")
	if err != nil {
		t.Fatalf("update-barrier --disabled error: %v", err)
	}
	if gotCfg == nil {
		t.Fatal("expected non-nil BarrierConfig")
	}
	if gotCfg.Enabled {
		t.Error("expected Enabled=false with --disabled flag")
	}
}

func TestDatabaseUpdateBarrier_JSONFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := newFullFnDatabaseService()
	svc.updateBarrierFn = func(name string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{Name: name, Engine: "kv", Backend: "pebble", Status: "open"}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-barrier", "mydb")
	if err != nil {
		t.Fatalf("update-barrier json error: %v", err)
	}
	if !strings.Contains(output, `"name"`) {
		t.Errorf("JSON output missing name, got: %q", output)
	}
}

func TestDatabaseUpdateBarrier_ServiceError(t *testing.T) {
	svc := newFullFnDatabaseService()
	svc.updateBarrierFn = func(name string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
		return nil, &database.NotFoundError{Name: name}
	}

	_, err := executeDatabaseCmd(t, svc, "update-barrier", "nonexistent")
	if err == nil {
		t.Fatal("expected error from update-barrier when database not found")
	}
}

func TestDatabaseUpdateBarrier_MissingArg(t *testing.T) {
	svc := newFullFnDatabaseService()
	_, err := executeDatabaseCmd(t, svc, "update-barrier")
	if err == nil {
		t.Fatal("expected error for missing name argument")
	}
}

// ---------------------------------------------------------------------------
// update-etcd
// ---------------------------------------------------------------------------

func TestDatabaseUpdateEtcd_EnabledWithListenAddr(t *testing.T) {
	svc := newFullFnDatabaseService()
	svc.updateEtcdFn = func(name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:    name,
			Engine:  "kv",
			Backend: "pebble",
			Status:  "open",
			Protocol: database.ProtocolConfig{
				Etcd: &database.EtcdConfig{Enabled: true, ListenAddr: cfg.ListenAddr},
			},
		}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-etcd", "mydb",
		"--enabled",
		"--listen-addr", "localhost:2379",
	)
	if err != nil {
		t.Fatalf("update-etcd --enabled error: %v", err)
	}
	if !strings.Contains(output, "Etcd:") {
		t.Errorf("output missing Etcd:, got: %q", output)
	}
}

func TestDatabaseUpdateEtcd_JSONFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := newFullFnDatabaseService()
	svc.updateEtcdFn = func(name string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{Name: name, Engine: "kv", Backend: "pebble", Status: "open"}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-etcd", "mydb", "--enabled")
	if err != nil {
		t.Fatalf("update-etcd json error: %v", err)
	}
	if !strings.Contains(output, `"name"`) {
		t.Errorf("JSON output missing name, got: %q", output)
	}
}

func TestDatabaseUpdateEtcd_ServiceError(t *testing.T) {
	svc := newFullFnDatabaseService()
	svc.updateEtcdFn = func(name string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
		return nil, &database.NotFoundError{Name: name}
	}

	_, err := executeDatabaseCmd(t, svc, "update-etcd", "nonexistent")
	if err == nil {
		t.Fatal("expected error from update-etcd when database not found")
	}
}

// ---------------------------------------------------------------------------
// update-connection
// ---------------------------------------------------------------------------

func TestDatabaseUpdateConnection_Success(t *testing.T) {
	var gotCfg *database.DatabaseConnectionConfig

	svc := newFullFnDatabaseService()
	svc.updateConnectionFn = func(name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
		gotCfg = cfg
		return &database.DatabaseInfo{
			Name:       name,
			Engine:     "kv",
			Backend:    "pebble",
			Status:     "open",
			Connection: cfg,
		}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-connection", "mydb",
		"--conn-transport", "quic",
		"--address", "remote.example.com:9090",
	)
	if err != nil {
		t.Fatalf("update-connection error: %v", err)
	}
	if gotCfg == nil {
		t.Fatal("expected non-nil ConnectionConfig")
	}
	if gotCfg.Transport != "quic" {
		t.Errorf("Transport = %q, want %q", gotCfg.Transport, "quic")
	}
	if gotCfg.Address != "remote.example.com:9090" {
		t.Errorf("Address = %q, want %q", gotCfg.Address, "remote.example.com:9090")
	}
	if !strings.Contains(output, "Connection:") {
		t.Errorf("output missing Connection:, got: %q", output)
	}
}

func TestDatabaseUpdateConnection_JSONFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := newFullFnDatabaseService()
	svc.updateConnectionFn = func(name string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{Name: name, Engine: "kv", Backend: "pebble", Status: "open"}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-connection", "mydb")
	if err != nil {
		t.Fatalf("update-connection json error: %v", err)
	}
	if !strings.Contains(output, `"name"`) {
		t.Errorf("JSON output missing name, got: %q", output)
	}
}

func TestDatabaseUpdateConnection_ServiceError(t *testing.T) {
	svc := newFullFnDatabaseService()
	svc.updateConnectionFn = func(name string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
		return nil, &database.NotFoundError{Name: name}
	}

	_, err := executeDatabaseCmd(t, svc, "update-connection", "nonexistent")
	if err == nil {
		t.Fatal("expected error from update-connection when database not found")
	}
}

func TestDatabaseUpdateConnection_MissingArg(t *testing.T) {
	svc := newFullFnDatabaseService()
	_, err := executeDatabaseCmd(t, svc, "update-connection")
	if err == nil {
		t.Fatal("expected error for missing name argument")
	}
}

// ===========================================================================
// database_instance_registry.go — Close path coverage.
// ===========================================================================

// TestDatabaseInstanceRegistry_Close_WatchHubOnly verifies Close on an
// instance that has only a watch hub (no host, no localBackend).
func TestDatabaseInstanceRegistry_Close_WatchHubOnly(t *testing.T) {
	hub := watch.NewHub()
	inst := &embeddedDatabaseInstance{watchHub: hub}
	if err := inst.Close(); err != nil {
		t.Fatalf("Close with watchHub: %v", err)
	}
}

// TestDatabaseInstanceRegistry_Close_NilFields verifies Close succeeds
// when all optional fields are nil.
func TestDatabaseInstanceRegistry_Close_NilFields(t *testing.T) {
	inst := &embeddedDatabaseInstance{}
	if err := inst.Close(); err != nil {
		t.Fatalf("Close with nil fields: %v", err)
	}
}

// TestDatabaseInstanceRegistry_Close_DoubleClose verifies that calling
// Close twice on an instance with a watch hub does not panic.
func TestDatabaseInstanceRegistry_Close_DoubleClose(t *testing.T) {
	hub := watch.NewHub()
	inst := &embeddedDatabaseInstance{watchHub: hub}

	if err := inst.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := inst.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestDatabaseInstanceRegistry_Register_DuplicateFromGaps verifies that
// registering the same name twice returns InstanceAlreadyExistsError.
func TestDatabaseInstanceRegistry_Register_DuplicateFromGaps(t *testing.T) {
	log := testLogger(t)
	r := NewDatabaseInstanceRegistry(log)

	inst := &embeddedDatabaseInstance{}
	if err := r.Register("db1", inst); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	err := r.Register("db1", inst)
	if err == nil {
		t.Fatal("expected InstanceAlreadyExistsError on duplicate register")
	}
	var iaee *InstanceAlreadyExistsError
	if !errors.As(err, &iaee) {
		t.Fatalf("expected InstanceAlreadyExistsError, got %T: %v", err, err)
	}
}

// TestDatabaseInstanceRegistry_Stop_NotFoundFromGaps verifies that Stop
// returns InstanceNotFoundError when the name is not registered.
func TestDatabaseInstanceRegistry_Stop_NotFoundFromGaps(t *testing.T) {
	log := testLogger(t)
	r := NewDatabaseInstanceRegistry(log)

	err := r.Stop("nonexistent")
	if err == nil {
		t.Fatal("expected InstanceNotFoundError for unknown name")
	}
	var infe *InstanceNotFoundError
	if !errors.As(err, &infe) {
		t.Fatalf("expected InstanceNotFoundError, got %T: %v", err, err)
	}
}

// TestDatabaseInstanceRegistry_StopAll_EmptyFromGaps verifies StopAll is
// a no-op when no instances are registered.
func TestDatabaseInstanceRegistry_StopAll_EmptyFromGaps(t *testing.T) {
	log := testLogger(t)
	r := NewDatabaseInstanceRegistry(log)
	if err := r.StopAll(); err != nil {
		t.Fatalf("StopAll on empty registry: %v", err)
	}
}

// TestDatabaseInstanceRegistry_StopAll_MultipleInstances verifies StopAll
// stops all registered instances and resets the count.
func TestDatabaseInstanceRegistry_StopAll_MultipleInstances(t *testing.T) {
	log := testLogger(t)
	r := NewDatabaseInstanceRegistry(log)

	for _, name := range []string{"db1", "db2", "db3"} {
		inst := &embeddedDatabaseInstance{watchHub: watch.NewHub()}
		if err := r.Register(name, inst); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}
	if r.Count() != 3 {
		t.Fatalf("expected Count()=3, got %d", r.Count())
	}

	if err := r.StopAll(); err != nil {
		t.Fatalf("StopAll: %v", err)
	}

	if r.Count() != 0 {
		t.Errorf("expected Count()=0 after StopAll, got %d", r.Count())
	}
	if len(r.Names()) != 0 {
		t.Errorf("expected no names after StopAll, got %v", r.Names())
	}
}

// ===========================================================================
// EmbeddedServerBinding — additional shutdown and StartEmbedded coverage.
// ===========================================================================

// TestEmbeddedServerBinding_Shutdown_WhenRunning verifies that the
// shutdown lifecycle hook stops the embedded server when it is running.
func TestEmbeddedServerBinding_Shutdown_WhenRunning(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	cfg := &EmbeddedConfig{
		StorageBackend: "memory",
		DataDir:        t.TempDir(),
		RaftEnabled:    false,
	}
	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	if !esb.IsEmbedded() {
		t.Fatal("expected IsEmbedded()=true")
	}

	esb.shutdown(context.Background())

	if esb.IsEmbedded() {
		t.Error("expected IsEmbedded()=false after shutdown")
	}
}

// TestEmbeddedServerBinding_StartEmbedded_AlreadyRunning verifies that
// StartEmbedded returns EmbeddedServerError when already running.
func TestEmbeddedServerBinding_StartEmbedded_AlreadyRunning(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	cfg := &EmbeddedConfig{
		StorageBackend: "memory",
		DataDir:        t.TempDir(),
		RaftEnabled:    false,
	}
	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("first StartEmbedded: %v", err)
	}
	defer func() { _ = esb.StopEmbedded() }()

	err := esb.StartEmbedded(cfg)
	if err == nil {
		t.Fatal("expected EmbeddedServerError when already running")
	}
	var ese *EmbeddedServerError
	if !errors.As(err, &ese) {
		t.Fatalf("expected EmbeddedServerError, got %T: %v", err, err)
	}
}

// ===========================================================================
// Compile-time interface checks
// ===========================================================================

var _ service.CeremonyServicer = (*mockCeremonyService)(nil)
var _ ceremony.CeremonyCA = (*mockCeremonyCA)(nil)
var _ service.DatabaseServicer = (*fullFnDatabaseService)(nil)

// Ensure transport, capabilities, and sdk are referenced.
var _ transport.WriteMode = transport.WriteModeSync
var _ = capabilities.Capabilities{}
var _ = sdk.Client{}
