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

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// shamirSplitRecorder answers every barrier init with result or err and
// records the options the handler passed, resolved. Every other
// TenantServicer method is the nil embedded interface: the init handler
// calls none of them.
type shamirSplitRecorder struct {
	tenant.TenantServicer
	result *tenant.TenantBarrierInitResult
	err    error
	calls  int
	got    tenant.TenantBarrierInitSettings
}

func (s *shamirSplitRecorder) TenantBarrierInitWithStrategy(_ context.Context, _ string, _ []byte, _, _ int, _ seal.StrategyID, _ *tenant.TenantBarrierShamirRoster, opts ...tenant.TenantBarrierInitOption) (*tenant.TenantBarrierInitResult, error) {
	s.calls++
	s.got = tenant.ResolveTenantBarrierInitOptions(opts...)
	return s.result, s.err
}

func newShamirSplitRecorder() *shamirSplitRecorder {
	return &shamirSplitRecorder{result: &tenant.TenantBarrierInitResult{
		TenantID: "acme", Strategy: "shamir", Threshold: 2, TotalShares: 2,
		Roster: []byte("roster"), UnsealKeyCheck: []byte("check"),
	}}
}

func callTenantBarrierInit(t *testing.T, svc tenant.TenantServicer, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	args["tenant_id"] = "acme"
	allow := func(context.Context, string, *authmw.TenantBarrierInitProof) error { return nil }
	return handleTenantBarrierInit(svc, allow)(context.Background(), buildCallToolRequest(t, "qrdb_tenant_barrier_init_complete", args))
}

func echo(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// The first request echoes nothing; it is answered with the split to sign.
func TestHandleTenantBarrierInit_FirstShamirRequestReturnsTheSplitAndEchoesNothing(t *testing.T) {
	svc := newShamirSplitRecorder()
	result, err := callTenantBarrierInit(t, svc, map[string]any{"strategy": "shamir", "threshold": 2, "shares": 2})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool error: %s", resultText(t, result))
	}
	if svc.got.PreparedRoster != nil || svc.got.PreparedUnsealKeyCheck != nil {
		t.Fatalf("service saw an echo %+v on the first request", svc.got)
	}
	var resp struct {
		Roster         []byte `json:"roster"`
		UnsealKeyCheck []byte `json:"unseal_key_check"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", resultText(t, result), err)
	}
	if string(resp.Roster) != "roster" || string(resp.UnsealKeyCheck) != "check" {
		t.Fatalf("response %s, want the prepared split", resultText(t, result))
	}
}

func TestHandleTenantBarrierInit_CarriesAPendingAdmin(t *testing.T) {
	for _, pending := range []bool{true, false} {
		svc := newShamirSplitRecorder()
		svc.result.AdminPending = pending
		result, err := callTenantBarrierInit(t, svc, map[string]any{"strategy": "shamir", "threshold": 2, "shares": 2})
		if err != nil {
			t.Fatalf("handler: %v", err)
		}
		if result.IsError {
			t.Fatalf("tool error: %s", resultText(t, result))
		}
		var resp struct {
			AdminPending bool `json:"admin_pending"`
		}
		if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
			t.Fatalf("unmarshal %s: %v", resultText(t, result), err)
		}
		if resp.AdminPending != pending {
			t.Fatalf("response %s, want admin_pending %v", resultText(t, result), pending)
		}
	}
}

// The second request's echo and statement reach the service unchanged.
func TestHandleTenantBarrierInit_SecondShamirRequestPassesTheEchoToTheService(t *testing.T) {
	svc := newShamirSplitRecorder()
	result, err := callTenantBarrierInit(t, svc, map[string]any{
		"strategy": "shamir", "threshold": 2, "shares": 2,
		"roster": echo("roster"), "unseal_key_check": echo("check"), "init_statement": echo("statement"),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool error: %s", resultText(t, result))
	}
	if svc.calls != 1 {
		t.Fatalf("service called %d times, want 1", svc.calls)
	}
	if string(svc.got.PreparedRoster) != "roster" || string(svc.got.PreparedUnsealKeyCheck) != "check" || string(svc.got.InitStatement) != "statement" {
		t.Fatalf("service saw %+v, want the echoed split and its statement", svc.got)
	}
}

// A mismatched echo is the caller's fault: FailedPrecondition, not Internal.
func TestHandleTenantBarrierInit_MismatchedEchoIsFailedPrecondition(t *testing.T) {
	svc := newShamirSplitRecorder()
	svc.err = &seal.PreparedShamirMismatchError{Field: "roster"}
	_, err := callTenantBarrierInit(t, svc, map[string]any{
		"strategy": "shamir", "roster": echo("other"), "unseal_key_check": echo("check"),
	})
	if err == nil {
		t.Fatal("a mismatched echo was accepted")
	}
	if code := ServiceErrorCode(err); code != wire.CodeFailedPrecondition {
		t.Fatalf("code %v, want CodeFailedPrecondition (err %v)", code, err)
	}
	if string(svc.got.PreparedRoster) != "other" {
		t.Fatalf("service saw %+v, want the caller's echo", svc.got)
	}
	if !strings.Contains(err.Error(), "roster") {
		t.Fatalf("error %q does not name the mismatched field", err)
	}
}

// An echo that is not base64 is refused before the service is called.
func TestHandleTenantBarrierInit_UndecodableEchoIsRefused(t *testing.T) {
	for _, field := range []string{"roster", "unseal_key_check"} {
		t.Run(field, func(t *testing.T) {
			svc := newShamirSplitRecorder()
			args := map[string]any{"strategy": "shamir", "roster": echo("roster"), "unseal_key_check": echo("check")}
			args[field] = "%%%"
			result, err := callTenantBarrierInit(t, svc, args)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if !result.IsError || !strings.Contains(resultText(t, result), field) {
				t.Fatalf("result %+v, want a tool error naming %s", result, field)
			}
			if svc.calls != 0 {
				t.Fatalf("service called %d times, want 0", svc.calls)
			}
		})
	}
}
