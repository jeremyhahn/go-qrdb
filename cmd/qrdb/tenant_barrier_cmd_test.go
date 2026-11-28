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
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Strategy-capturing tenant service mock
//
// mockTenantService.TenantBarrierInitWithStrategy (bindings_tenant_test.go)
// delegates to TenantBarrierInit and discards the strategy argument, so it
// cannot observe --strategy/--passphrase. strategyCapturingTenantService
// embeds mockTenantService and overrides TenantBarrierInitWithStrategy to
// record every argument the CLI sends, without modifying the shared mock in
// bindings_tenant_test.go or tenant_test.go.
// ---------------------------------------------------------------------------

// capturedBarrierInitCall records the arguments TenantBarrierInitWithStrategy
// was invoked with.
type capturedBarrierInitCall struct {
	tenantID   string
	passphrase []byte
	threshold  int
	shares     int
	strategy   seal.StrategyID
}

// strategyCapturingTenantService wraps mockTenantService to additionally
// capture the strategy argument passed to TenantBarrierInitWithStrategy, and
// optionally synthesize a Shamir-shaped result (threshold/total_shares/
// share_count) so the CLI's structured (non-OK) output path can be exercised.
type strategyCapturingTenantService struct {
	*mockTenantService

	captured *capturedBarrierInitCall

	// resultFn, when set, overrides the synthesized TenantBarrierInitResult.
	resultFn func(call capturedBarrierInitCall) (*tenant.TenantBarrierInitResult, error)

	// shamirCompleted records that a Shamir init's second request echoed
	// the split the first one prepared.
	shamirCompleted bool
}

func (m *strategyCapturingTenantService) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return tenant.ClusterIsolationPolicyOperatorAccess, nil
}

func (m *strategyCapturingTenantService) SetClusterIsolationPolicy(context.Context, tenant.ClusterIsolationPolicy) error {
	return nil
}

// mockPreparedRoster and mockPreparedUnsealKeyCheck are the split the
// strategy-capturing mock prepares on a Shamir init's first request.
var (
	mockPreparedRoster         = []byte("mock prepared roster")
	mockPreparedUnsealKeyCheck = []byte("mock prepared unseal key check")
)

func newStrategyCapturingTenantService() *strategyCapturingTenantService {
	return &strategyCapturingTenantService{mockTenantService: &mockTenantService{}}
}

// TenantBarrierInitWithStrategy overrides the promoted mockTenantService
// method to capture the strategy argument (which TenantBarrierInitWithStrategy
// on the embedded mock silently drops) and to let each test control the
// returned TenantBarrierInitResult.
func (m *strategyCapturingTenantService) TenantBarrierInitWithStrategy(
	ctx context.Context,
	tenantID string,
	passphrase []byte,
	threshold, shares int,
	strategy seal.StrategyID,
	_ *tenant.TenantBarrierShamirRoster,
	opts ...tenant.TenantBarrierInitOption,
) (*tenant.TenantBarrierInitResult, error) {
	// passphrase is a BORROW valid only for this call — the CLI zeroizes the
	// request's slice once TenantBarrierInit returns — so the recorded copy
	// must be deep. Recording the slice header would hand the assertions a
	// buffer that is legitimately wiped out from under them.
	call := capturedBarrierInitCall{
		tenantID:   tenantID,
		passphrase: bytes.Clone(passphrase),
		threshold:  threshold,
		shares:     shares,
		strategy:   strategy,
	}
	m.captured = &call

	if m.resultFn != nil {
		return m.resultFn(call)
	}

	// Default: mirror pkg/tenant/service.go TenantBarrierInitWithStrategy --
	// only the Shamir strategy returns share metadata; every other strategy
	// (including the empty-string default, which the server resolves to
	// passphrase) returns a bare result with no share fields.
	//
	// A Shamir init is two requests (tenantBarrierInitShamir): the first
	// prepares the split and returns its roster and unseal key check value,
	// the second echoes them and completes, and an echo of any other split is
	// refused. Both answers carry the split.
	if strategy == seal.StrategyShamir {
		settings := tenant.ResolveTenantBarrierInitOptions(opts...)
		if settings.PreparedRoster != nil || settings.PreparedUnsealKeyCheck != nil {
			if !bytes.Equal(settings.PreparedUnsealKeyCheck, mockPreparedUnsealKeyCheck) {
				return nil, &seal.PreparedShamirMismatchError{Field: "unseal key check"}
			}
			if !bytes.Equal(settings.PreparedRoster, mockPreparedRoster) {
				return nil, &seal.PreparedShamirMismatchError{Field: "roster"}
			}
			m.shamirCompleted = true
		}
		return &tenant.TenantBarrierInitResult{
			TenantID:       tenantID,
			Strategy:       string(strategy),
			Threshold:      threshold,
			TotalShares:    shares,
			ShareCount:     shares,
			UnsealKeyCheck: mockPreparedUnsealKeyCheck,
			Roster:         mockPreparedRoster,
		}, nil
	}
	return &tenant.TenantBarrierInitResult{TenantID: tenantID, Strategy: string(strategy)}, nil
}

// executeTenantCmdOut mirrors executeTenantCmd (tenant_test.go) but lets the
// caller select the output format, needed to assert the JSON/YAML rendering
// of the Shamir barrier-init response.
func executeTenantCmdOut(t *testing.T, svc tenant.TenantServicer, format string, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithTenantService(svc))
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
	outputFormat = format
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newTenantCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// ---------------------------------------------------------------------------
// tenant barrier-init --strategy / --passphrase parity tests
// ---------------------------------------------------------------------------

// TestTenantBarrierInitCmd_PassphraseStrategyFlags verifies --strategy and
// --passphrase are threaded through to the service call, closing the CLI/GUI
// parity gap (the GUI's TenantBinding.BarrierInit / TS SDK barrierInit both
// send strategy+passphrase on every call; the CLI previously had no flags for
// either, so it could never satisfy pkg/tenant/service.go's
// ErrEmptyPassphrase requirement for the default passphrase strategy).
func TestTenantBarrierInitCmd_PassphraseStrategyFlags(t *testing.T) {
	svc := newStrategyCapturingTenantService()
	out, err := executeTenantCmd(t, svc, "barrier-init", "--id", "tenant-001",
		"--strategy", "passphrase", "--passphrase", "s3cr3t-passphrase")
	if err != nil {
		t.Fatalf("tenant barrier-init --strategy passphrase --passphrase: %v", err)
	}
	if svc.captured == nil {
		t.Fatal("expected TenantBarrierInitWithStrategy to be called")
	}
	if svc.captured.strategy != seal.StrategyPassphrase {
		t.Fatalf("expected strategy=passphrase, got %q", svc.captured.strategy)
	}
	if string(svc.captured.passphrase) != "s3cr3t-passphrase" {
		t.Fatalf("expected passphrase to be threaded through, got %q", svc.captured.passphrase)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK in output for a non-shamir strategy, got: %s", out)
	}
}

// TestTenantBarrierInitCmd_NoneStrategyOmitsPassphrase verifies --strategy
// none does not require --passphrase (mirrors
// seal.IsPassphraseStrategy / pkg/tenant/service.go: only the passphrase
// strategy requires caller-supplied key material).
func TestTenantBarrierInitCmd_NoneStrategyOmitsPassphrase(t *testing.T) {
	svc := newStrategyCapturingTenantService()
	out, err := executeTenantCmd(t, svc, "barrier-init", "--id", "tenant-001", "--strategy", "none")
	if err != nil {
		t.Fatalf("tenant barrier-init --strategy none: %v", err)
	}
	if svc.captured == nil {
		t.Fatal("expected TenantBarrierInitWithStrategy to be called")
	}
	if svc.captured.strategy != seal.StrategyNone {
		t.Fatalf("expected strategy=none, got %q", svc.captured.strategy)
	}
	if string(svc.captured.passphrase) != "" {
		t.Fatalf("expected empty passphrase for the none strategy, got %q", svc.captured.passphrase)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK in output, got: %s", out)
	}
}

// TestTenantBarrierInitCmd_DefaultStrategyIsEmpty verifies the CLI does not
// inject a client-side default for --strategy: an omitted flag sends an
// empty Strategy field on the wire, matching the documented "empty =
// passphrase" contract that the SERVER resolves
// (TenantBarrierInitWithStrategy: `if strategyID == "" { strategyID =
// seal.StrategyPassphrase }`).
func TestTenantBarrierInitCmd_DefaultStrategyIsEmpty(t *testing.T) {
	svc := newStrategyCapturingTenantService()
	_, err := executeTenantCmd(t, svc, "barrier-init", "--id", "tenant-001")
	if err != nil {
		t.Fatalf("tenant barrier-init (no --strategy): %v", err)
	}
	if svc.captured == nil {
		t.Fatal("expected TenantBarrierInitWithStrategy to be called")
	}
	if svc.captured.strategy != "" {
		t.Fatalf("expected empty strategy (server-resolved default), got %q", svc.captured.strategy)
	}
}

// TestTenantBarrierInitCmd_ShamirStrategyKeepsThresholdShares verifies
// --threshold/--shares remain the Shamir M-of-N configuration knobs when
// --strategy=shamir, and that the returned share metadata (never share
// bytes) is rendered in table output.
func TestTenantBarrierInitCmd_ShamirStrategyKeepsThresholdShares(t *testing.T) {
	svc := newStrategyCapturingTenantService()
	out, err := executeTenantCmd(t, svc, "barrier-init", "--id", "tenant-001",
		"--strategy", "shamir", "--threshold", "2", "--shares", "3")
	if err != nil {
		t.Fatalf("tenant barrier-init --strategy shamir: %v", err)
	}
	if svc.captured == nil {
		t.Fatal("expected TenantBarrierInitWithStrategy to be called")
	}
	if svc.captured.strategy != seal.StrategyShamir {
		t.Fatalf("expected strategy=shamir, got %q", svc.captured.strategy)
	}
	if svc.captured.threshold != 2 || svc.captured.shares != 3 {
		t.Fatalf("expected threshold=2 shares=3, got threshold=%d shares=%d",
			svc.captured.threshold, svc.captured.shares)
	}
	if !svc.shamirCompleted {
		t.Fatal("expected a second request echoing the prepared split")
	}
	// No share bytes are ever printed (INV-1); only the enrollment metadata.
	if strings.Contains(out, "share_bytes") || strings.Contains(out, "ShareBytes") {
		t.Fatalf("output must never contain share bytes: %s", out)
	}
	if !strings.Contains(out, "ShareCount:  3") {
		t.Fatalf("expected ShareCount: 3 in table output, got: %s", out)
	}
	if !strings.Contains(out, "Threshold:   2") {
		t.Fatalf("expected Threshold: 2 in table output, got: %s", out)
	}
}

// TestTenantBarrierInitCmd_ShamirStrategyJSONOutput verifies --output json
// renders the full TenantBarrierInitResponse (tenant_id/strategy/threshold/
// total_shares/share_count) for the Shamir strategy, matching the REST/TS-SDK
// wire contract (sdk/typescript/src/types.ts TenantBarrierInitResponse).
func TestTenantBarrierInitCmd_ShamirStrategyJSONOutput(t *testing.T) {
	svc := newStrategyCapturingTenantService()
	out, err := executeTenantCmdOut(t, svc, "json", "barrier-init", "--id", "tenant-001",
		"--strategy", "shamir", "--threshold", "2", "--shares", "3")
	if err != nil {
		t.Fatalf("tenant barrier-init --strategy shamir --output json: %v", err)
	}
	for _, field := range []string{`"tenant_id"`, `"strategy"`, `"threshold"`, `"total_shares"`, `"share_count"`} {
		if !strings.Contains(out, field) {
			t.Fatalf("expected JSON field %s in output, got: %s", field, out)
		}
	}
	if strings.Contains(out, "share_bytes") {
		t.Fatalf("JSON output must never contain share bytes: %s", out)
	}
}

// TestTenantBarrierInitCmd_EmptyPassphraseServiceError verifies the typed
// server-side rejection (pkg/tenant.ErrEmptyPassphrase, surfaced through
// TenantBarrierInitWithStrategy) propagates as a CLI error when --passphrase
// is omitted against the passphrase strategy -- this is the exact failure
// this parity fix makes reachable/fixable by the operator (previously the
// CLI could never supply --passphrase at all).
func TestTenantBarrierInitCmd_EmptyPassphraseServiceError(t *testing.T) {
	svc := newStrategyCapturingTenantService()
	svc.resultFn = func(call capturedBarrierInitCall) (*tenant.TenantBarrierInitResult, error) {
		if call.strategy == seal.StrategyPassphrase || call.strategy == "" {
			if string(call.passphrase) == "" {
				return nil, tenant.ErrEmptyPassphrase
			}
		}
		return &tenant.TenantBarrierInitResult{TenantID: call.tenantID, Strategy: string(call.strategy)}, nil
	}

	_, err := executeTenantCmd(t, svc, "barrier-init", "--id", "tenant-001", "--strategy", "passphrase")
	if err == nil {
		t.Fatal("expected ErrEmptyPassphrase when --passphrase is omitted for the passphrase strategy")
	}
	if !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("expected passphrase-related error, got: %v", err)
	}
}
