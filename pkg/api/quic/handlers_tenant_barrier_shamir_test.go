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
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// shamirSplitTenantSvc answers every init with result.
type shamirSplitTenantSvc struct {
	*mockTenantService
	result *tenant.TenantBarrierInitResult
}

func (s *shamirSplitTenantSvc) TenantBarrierInitWithStrategy(context.Context, string, []byte, int, int, seal.StrategyID, *tenant.TenantBarrierShamirRoster, ...tenant.TenantBarrierInitOption) (*tenant.TenantBarrierInitResult, error) {
	return s.result, nil
}

func newShamirSplitServer(t *testing.T) (*Server, *shamirSplitTenantSvc) {
	t.Helper()
	svc := &shamirSplitTenantSvc{mockTenantService: &mockTenantService{}, result: &tenant.TenantBarrierInitResult{
		TenantID: "acme", Strategy: "shamir", Threshold: 2, TotalShares: 2,
		Roster: []byte("roster"), UnsealKeyCheck: []byte("check"),
	}}
	return newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc)), svc
}

func TestHandleTenantBarrierInit_ReturnsTheShamirSplit(t *testing.T) {
	srv, _ := newShamirSplitServer(t)
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "acme", Strategy: "shamir"})

	out, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleTenantBarrierInit: %v", err)
	}
	var resp quicTenantBarrierInitResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(resp.Roster) != "roster" || string(resp.UnsealKeyCheck) != "check" || resp.Threshold != 2 {
		t.Fatalf("resp = %+v, want the prepared split", resp)
	}
}

func TestHandleTenantBarrierInit_CarriesAPendingAdmin(t *testing.T) {
	srv, svc := newShamirSplitServer(t)
	svc.result.AdminPending = true
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "acme", Strategy: "shamir"})

	out, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleTenantBarrierInit: %v", err)
	}
	var resp quicTenantBarrierInitResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.AdminPending {
		t.Fatalf("resp = %+v, want admin_pending", resp)
	}
}

func TestHandleTenantBarrierInit_CompletedAdminIsNotPending(t *testing.T) {
	srv, _ := newShamirSplitServer(t)
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "acme", Strategy: "shamir"})

	out, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleTenantBarrierInit: %v", err)
	}
	var resp quicTenantBarrierInitResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.AdminPending {
		t.Fatalf("resp = %+v, want no pending admin", resp)
	}
}

func TestHandleTenantBarrierInit_NonShamirOmitsTheSplit(t *testing.T) {
	srv, svc := newShamirSplitServer(t)
	svc.result = &tenant.TenantBarrierInitResult{TenantID: "acme", Strategy: "passphrase"}
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "acme", Strategy: "passphrase"})

	out, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleTenantBarrierInit: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := fields["roster"]; ok {
		t.Errorf("response %s carries a roster", out)
	}
	if _, ok := fields["unseal_key_check"]; ok {
		t.Errorf("response %s carries an unseal key check", out)
	}
}
