//go:build !integration

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
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// mockAuditLogger — configurable audit.AuditLogger mock
// ---------------------------------------------------------------------------

type mockAuditLogger struct {
	emitted []audit.AuditEvent
	queryFn func(ctx context.Context, f audit.Filter) (*audit.Page, error)
}

var _ audit.AuditLogger = (*mockAuditLogger)(nil)

func (m *mockAuditLogger) Emit(_ context.Context, ev audit.AuditEvent) error {
	m.emitted = append(m.emitted, ev)
	return nil
}

func (m *mockAuditLogger) Query(ctx context.Context, f audit.Filter) (*audit.Page, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, f)
	}
	return &audit.Page{}, nil
}

func (m *mockAuditLogger) Close() error {
	return nil
}

// mockVerifyingAuditLogger extends mockAuditLogger with VerifyChainSet, so
// it satisfies audit.Verifier for "audit verify" tests. mockAuditLogger
// itself deliberately does NOT implement Verifier, so it doubles for a
// backend with no hash chain (RingBuffer, NoopLogger, SlogSink).
type mockVerifyingAuditLogger struct {
	mockAuditLogger
	verifyFn func(ctx context.Context) (*audit.ChainVerification, error)
}

func (m *mockVerifyingAuditLogger) VerifyChainSet(ctx context.Context) (*audit.ChainVerification, error) {
	return m.verifyFn(ctx)
}

var _ audit.Verifier = (*mockVerifyingAuditLogger)(nil)

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// executeAuditCmd builds an embedded SDK client with the given audit
// logger, sets the package-level client, builds the "audit" command
// tree, and executes it with the given args.
func executeAuditCmd(t *testing.T, logger audit.AuditLogger, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(),
		embedded.WithAuditLogger(logger),
	)
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
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
	})

	client = sdkClient
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newAuditCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// ---------------------------------------------------------------------------
// audit query
// ---------------------------------------------------------------------------

func TestAuditQuery_Table(t *testing.T) {
	ts := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, _ audit.Filter) (*audit.Page, error) {
			return &audit.Page{
				Events: []audit.AuditEvent{
					{
						Timestamp: ts,
						Actor:     "alice",
						TenantID:  "acme",
						Role:      "admin",
						Resource:  "barrier",
						Action:    "unseal",
						Outcome:   "success",
					},
				},
				Total: 1,
			}, nil
		},
	}

	output, err := executeAuditCmd(t, logger, "query", "--actor", "alice")
	if err != nil {
		t.Fatalf("audit query error: %v", err)
	}
	for _, want := range []string{"TIMESTAMP", "ACTOR", "alice", "acme", "barrier", "unseal", "success", "Total: 1"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestAuditQuery_JSON(t *testing.T) {
	var captured audit.Filter
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, f audit.Filter) (*audit.Page, error) {
			captured = f
			return &audit.Page{
				Events: []audit.AuditEvent{{Actor: "bob", Resource: "kv", Action: "put", Outcome: "success"}},
				Total:  1,
				Next:   "cursor-2",
			}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeAuditCmd(t, logger, "query",
		"--tenant-id", "system",
		"--resource", "kv",
		"--action", "put",
		"--outcome", "success",
		"--limit", "50",
		"--cursor", "cursor-1",
		"--from", "2026-01-01T00:00:00Z",
		"--to", "2026-12-31T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("audit query json error: %v", err)
	}

	var page transport.AuditPage
	if jsonErr := json.Unmarshal([]byte(output), &page); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if page.Next != "cursor-2" {
		t.Errorf("Next = %q, want cursor-2", page.Next)
	}
	if captured.TenantID != "system" || captured.Resource != "kv" || captured.Action != "put" {
		t.Errorf("captured filter = %+v", captured)
	}
	if captured.Limit != 50 || captured.Cursor != "cursor-1" {
		t.Errorf("captured filter pagination = %+v", captured)
	}
	if captured.From.IsZero() || captured.To.IsZero() {
		t.Errorf("captured filter time range = %+v, want non-zero From/To", captured)
	}
}

func TestAuditQuery_InvalidFromTime(t *testing.T) {
	logger := &mockAuditLogger{}
	_, err := executeAuditCmd(t, logger, "query", "--from", "not-a-timestamp")
	if err == nil {
		t.Fatal("expected error for invalid --from timestamp")
	}
	var timeErr *InvalidTimeRangeError
	if !errors.As(err, &timeErr) {
		t.Errorf("expected *InvalidTimeRangeError, got %T: %v", err, err)
	}
}

func TestAuditQuery_InvalidToTime(t *testing.T) {
	logger := &mockAuditLogger{}
	_, err := executeAuditCmd(t, logger, "query", "--to", "garbage")
	if err == nil {
		t.Fatal("expected error for invalid --to timestamp")
	}
	var timeErr *InvalidTimeRangeError
	if !errors.As(err, &timeErr) {
		t.Errorf("expected *InvalidTimeRangeError, got %T: %v", err, err)
	}
}

func TestAuditQuery_ServiceError(t *testing.T) {
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, _ audit.Filter) (*audit.Page, error) {
			return nil, errors.New("query failed")
		},
	}
	_, err := executeAuditCmd(t, logger, "query")
	if err == nil {
		t.Fatal("expected error from query command")
	}
}

func TestAuditQuery_NoLoggerConfigured(t *testing.T) {
	tr, err := embedded.New(&noopKVService{}, newMockAdminService())
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	t.Cleanup(func() { client = oldClient })
	client = sdkClient

	cmd := newAuditCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"query"})

	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected error when no audit logger is configured")
	}
}

// ---------------------------------------------------------------------------
// parseAuditTime — direct unit tests
// ---------------------------------------------------------------------------

func TestParseAuditTime_Valid(t *testing.T) {
	got, err := parseAuditTime("--from", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("parseAuditTime: %v", err)
	}
	if got.Year() != 2026 {
		t.Errorf("Year = %d, want 2026", got.Year())
	}
}

func TestParseAuditTime_Invalid(t *testing.T) {
	_, err := parseAuditTime("--to", "not-a-date")
	if err == nil {
		t.Fatal("expected error for invalid timestamp")
	}
}

// ---------------------------------------------------------------------------
// audit verify
// ---------------------------------------------------------------------------

func TestAuditVerify_Verified(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 2, Records: 10, Genesis: true}, nil
		},
	}
	output, err := executeAuditCmd(t, logger, "verify")
	if err != nil {
		t.Fatalf("audit verify error: %v", err)
	}
	for _, want := range []string{"Verified: true", "Files: 2", "Records: 10", "Genesis: true"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestAuditVerify_JSON(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: 3, Genesis: true, Handoff: "cafe"}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeAuditCmd(t, logger, "verify")
	if err != nil {
		t.Fatalf("audit verify json error: %v", err)
	}
	var result transport.AuditVerifyResult
	if jsonErr := json.Unmarshal([]byte(output), &result); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if !result.Verified || result.Handoff != "cafe" {
		t.Errorf("result = %+v", result)
	}
}

// TestAuditVerify_ChainBroken asserts that a tamper verdict exits non-zero
// (RunE returns an error) while still printing the report, so an operator
// or a monitoring script sees both the detection and a failing exit status.
func TestAuditVerify_ChainBroken(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: 3},
				&audit.ChainBrokenError{Path: "audit-events.ndjson", Index: 2, Expected: "aa", Got: "bb"}
		},
	}
	output, err := executeAuditCmd(t, logger, "verify")
	if err == nil {
		t.Fatal("expected a non-nil error for a broken chain (non-zero exit)")
	}
	var tamperedErr *AuditChainTamperedError
	if !errors.As(err, &tamperedErr) {
		t.Fatalf("expected *AuditChainTamperedError, got %T: %v", err, err)
	}
	if !strings.Contains(output, "Verified: false") {
		t.Errorf("output missing tamper report, got: %q", output)
	}
}

// TestAuditVerify_UnsupportedBackend uses mockAuditLogger, which
// deliberately does not implement audit.Verifier. The embedded transport
// maps the resulting *audit.VerificationUnsupportedError to the SAME
// *transport.NotImplementedError the network transports surface (grpc/unix
// map codes.Unimplemented, REST maps 501), so a caller checks one type
// regardless of which transport is active.
func TestAuditVerify_UnsupportedBackend(t *testing.T) {
	logger := &mockAuditLogger{}
	_, err := executeAuditCmd(t, logger, "verify")
	if err == nil {
		t.Fatal("expected error for a backend with no hash chain")
	}
	var unsupported *transport.NotImplementedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *transport.NotImplementedError, got %T: %v", err, err)
	}
}

func TestAuditVerify_NoLoggerConfigured(t *testing.T) {
	tr, err := embedded.New(&noopKVService{}, newMockAdminService())
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	t.Cleanup(func() { client = oldClient })
	client = sdkClient

	cmd := newAuditCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"verify"})

	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected error when no audit logger is configured")
	}
}

// TestAuditVerify_YAML renders the verdict as YAML.
func TestAuditVerify_YAML(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: 4, Genesis: true}, nil
		},
	}
	oldFormat := outputFormat
	outputFormat = "yaml"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeAuditCmd(t, logger, "verify")
	if err != nil {
		t.Fatalf("audit verify yaml error: %v", err)
	}
	if !strings.Contains(output, "verified: true") || !strings.Contains(output, "records: 4") {
		t.Fatalf("yaml output = %q", output)
	}
}

// TestPrintAuditVerifyResult_EveryField: the table names the reason, the
// file order, the seed and the handoff when the verdict carries them, and
// omits each line when it does not.
func TestPrintAuditVerifyResult_EveryField(t *testing.T) {
	oldWriter := outputWriter
	t.Cleanup(func() { outputWriter = oldWriter })

	var full bytes.Buffer
	outputWriter = &full
	printAuditVerifyResult(&transport.AuditVerifyResult{
		Reason: "hash mismatch at record 2", Files: 2, Records: 7,
		Order: []string{"a.ndjson", "b.ndjson"}, Seed: "5eed", Handoff: "cafe",
	})
	for _, want := range []string{"Verified: false", "Reason: hash mismatch at record 2", "Order: a.ndjson, b.ndjson", "Seed: 5eed", "Handoff: cafe"} {
		if !strings.Contains(full.String(), want) {
			t.Errorf("output missing %q: %q", want, full.String())
		}
	}

	var bare bytes.Buffer
	outputWriter = &bare
	printAuditVerifyResult(&transport.AuditVerifyResult{Verified: true})
	for _, absent := range []string{"Reason:", "Order:", "Seed:", "Handoff:"} {
		if strings.Contains(bare.String(), absent) {
			t.Errorf("an empty field printed %q: %q", absent, bare.String())
		}
	}
}

// ---------------------------------------------------------------------------
// audit query/verify --replicated
// ---------------------------------------------------------------------------

// fakeReplicatedInner implements audit.AuditLogger and
// service.TenantChainVerifier, standing in for a *service.ReplicatedLogger.
type fakeReplicatedInner struct {
	mockAuditLogger
	verifyReport *audit.ChainContinuityReport
}

func (f *fakeReplicatedInner) VerifyTenantChain(context.Context, string) (*audit.ChainContinuityReport, error) {
	return f.verifyReport, nil
}

// executeAuditCmdReplicated builds an embedded SDK client with both a
// node-local logger and a replicated audit sink wired, and executes the
// "audit" command tree. The embedded transport binds no identity, so the
// caller always resolves to the system tenant (platformEnabled governs it).
func executeAuditCmdReplicated(t *testing.T, logger audit.AuditLogger, replicatedAudit *service.OptInAuditLogger, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(),
		embedded.WithAuditLogger(logger),
		embedded.WithReplicatedAuditLogger(replicatedAudit),
	)
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
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
	})

	client = sdkClient
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newAuditCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// fakePlatformEntitlement answers ReplicatedAuditEnabled unconditionally;
// the embedded transport's unbound caller resolves to the system tenant,
// whose posture OptInAuditLogger reads from platformEnabled, not this
// lookup -- so its answer is never consulted in these tests, only its
// presence (a nil AuditEntitlementLookup would panic for a non-system
// caller).
type fakePlatformEntitlement struct{}

func (fakePlatformEntitlement) ReplicatedAuditEnabled(context.Context, string) (bool, error) {
	return false, nil
}

func TestAuditQuery_Replicated_ReadsReplicatedLoggerNotNodeLocal(t *testing.T) {
	nodeLocal := &mockAuditLogger{queryFn: func(context.Context, audit.Filter) (*audit.Page, error) {
		t.Fatal("node-local logger must not be queried when --replicated is set")
		return nil, nil
	}}
	inner := &fakeReplicatedInner{mockAuditLogger: mockAuditLogger{
		queryFn: func(_ context.Context, f audit.Filter) (*audit.Page, error) {
			return &audit.Page{Events: []audit.AuditEvent{{Actor: "op"}}, Total: -1}, nil
		},
	}}
	replicated := service.NewOptInAuditLogger(inner, fakePlatformEntitlement{}, true)

	output, err := executeAuditCmdReplicated(t, nodeLocal, replicated, "query", "--replicated")
	if err != nil {
		t.Fatalf("audit query --replicated: %v", err)
	}
	if !strings.Contains(output, "op") {
		t.Errorf("output missing the replicated logger's event, got: %q", output)
	}
}

func TestAuditQuery_Replicated_NotEntitled(t *testing.T) {
	inner := &fakeReplicatedInner{}
	replicated := service.NewOptInAuditLogger(inner, fakePlatformEntitlement{}, false)

	output, err := executeAuditCmdReplicated(t, &mockAuditLogger{}, replicated, "query", "--replicated")
	if err != nil {
		t.Fatalf("audit query --replicated: %v", err)
	}
	if !strings.Contains(output, "Total: -1") {
		t.Errorf("output missing Total: -1 for a not-entitled tenant, got: %q", output)
	}
}

func TestAuditVerify_Replicated_Verified(t *testing.T) {
	inner := &fakeReplicatedInner{verifyReport: &audit.ChainContinuityReport{Verified: true, Records: 4, Head: 4, Low: 1, Genesis: true}}
	replicated := service.NewOptInAuditLogger(inner, fakePlatformEntitlement{}, true)

	output, err := executeAuditCmdReplicated(t, &mockAuditLogger{}, replicated, "verify", "--replicated")
	if err != nil {
		t.Fatalf("audit verify --replicated: %v", err)
	}
	for _, want := range []string{"Verified: true", "Records: 4", "Head: 4", "Low: 1", "Genesis: true"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestAuditVerify_Replicated_NotEntitledIsAnError(t *testing.T) {
	inner := &fakeReplicatedInner{}
	replicated := service.NewOptInAuditLogger(inner, fakePlatformEntitlement{}, false)

	_, err := executeAuditCmdReplicated(t, &mockAuditLogger{}, replicated, "verify", "--replicated")
	if err == nil {
		t.Fatal("expected an error for a tenant not entitled to the replicated chain")
	}
}

func TestAuditVerify_Replicated_Tampered(t *testing.T) {
	inner := &fakeReplicatedInner{verifyReport: &audit.ChainContinuityReport{Verified: false, Reason: "broken link at sequence 3"}}
	replicated := service.NewOptInAuditLogger(inner, fakePlatformEntitlement{}, true)

	output, err := executeAuditCmdReplicated(t, &mockAuditLogger{}, replicated, "verify", "--replicated")
	var tampered *AuditChainTamperedError
	if !errors.As(err, &tampered) {
		t.Fatalf("expected *AuditChainTamperedError, got %v", err)
	}
	if !strings.Contains(output, "Verified: false") || !strings.Contains(output, "broken link at sequence 3") {
		t.Errorf("output missing the tamper report, got: %q", output)
	}
}
