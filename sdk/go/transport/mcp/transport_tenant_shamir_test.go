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
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// shamirSplitRecorder answers a Shamir barrier init the way a node does: the
// first request is answered with the prepared split, the second, which must
// echo it, completes. It records the options of every request, resolved.
// Every other TenantServicer method is the nil embedded interface.
type shamirSplitRecorder struct {
	tenant.TenantServicer
	mu        sync.Mutex
	secondErr error
	got       []tenant.TenantBarrierInitSettings
}

func (s *shamirSplitRecorder) TenantBarrierInitWithStrategy(_ context.Context, id string, _ []byte, _, _ int, _ seal.StrategyID, _ *tenant.TenantBarrierShamirRoster, opts ...tenant.TenantBarrierInitOption) (*tenant.TenantBarrierInitResult, error) {
	s.mu.Lock()
	s.got = append(s.got, tenant.ResolveTenantBarrierInitOptions(opts...))
	n := len(s.got)
	s.mu.Unlock()
	if n > 1 && s.secondErr != nil {
		return nil, s.secondErr
	}
	return &tenant.TenantBarrierInitResult{
		TenantID: id, Strategy: "shamir", Threshold: 2, TotalShares: 2,
		Roster: []byte("roster"), UnsealKeyCheck: []byte("check"),
	}, nil
}

// calls returns a copy of the requests recorded so far. The server answers
// on its own goroutine, so the test reads them under the same lock.
func (s *shamirSplitRecorder) calls() []tenant.TenantBarrierInitSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tenant.TenantBarrierInitSettings(nil), s.got...)
}

func shamirInitRequest(t *testing.T) *transport.TenantBarrierInitRequest {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: "shamir", Threshold: 2, Shares: 2, Signer: key}
}

// The first request echoes nothing and gets the split; the second carries
// the split back, with the owner's statement over it.
func TestTenantBarrierInit_ShamirRoundTripEchoesTheSplit(t *testing.T) {
	svc := &shamirSplitRecorder{}
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	resp, err := tr.TenantBarrierInit(context.Background(), shamirInitRequest(t))
	if err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}
	got := svc.calls()
	if len(got) != 2 {
		t.Fatalf("service called %d times, want 2", len(got))
	}
	if first := got[0]; first.PreparedRoster != nil || first.PreparedUnsealKeyCheck != nil || first.InitStatement != nil {
		t.Fatalf("first request carried %+v, want no echo", first)
	}
	second := got[1]
	if string(second.PreparedRoster) != "roster" || string(second.PreparedUnsealKeyCheck) != "check" {
		t.Fatalf("second request carried %+v, want the first answer's split", second)
	}
	if len(second.InitStatement) == 0 {
		t.Fatal("second request carried no statement over the split")
	}
	if string(resp.Roster) != "roster" || string(resp.UnsealKeyCheck) != "check" {
		t.Fatalf("response %+v, want the split", resp)
	}
}

// A node that refuses the echo fails the init as a failed precondition.
func TestTenantBarrierInit_ShamirMismatchedEchoIsRefused(t *testing.T) {
	svc := &shamirSplitRecorder{secondErr: &seal.PreparedShamirMismatchError{Field: "roster"}}
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	_, err := tr.TenantBarrierInit(context.Background(), shamirInitRequest(t))
	if err == nil {
		t.Fatal("a refused echo was reported as success")
	}
	if !strings.Contains(err.Error(), "failed precondition") || !strings.Contains(err.Error(), "roster") {
		t.Fatalf("error %q, want a failed precondition naming the roster", err)
	}
	got := svc.calls()
	if len(got) != 2 {
		t.Fatalf("service called %d times, want 2", len(got))
	}
}
