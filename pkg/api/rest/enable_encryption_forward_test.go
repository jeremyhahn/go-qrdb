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

package rest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestHandleBarrierEnableEncryption_FollowerForwardsToLeader pins node parity
// for enable-encryption: a node whose cluster migrator refuses because it does
// not lead the coordinator's shard replays the identical request at the leader
// and relays the leader's answer, rather than handing the client the refusal
// (main red, run 56290: a 500 during a system-shard election).
func TestHandleBarrierEnableEncryption_FollowerForwardsToLeader(t *testing.T) {
	var leaderCalls atomic.Int32
	var leaderSawBody atomic.Value
	leader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaderCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		leaderSawBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"migration_started","scope":"tenant"}`))
	}))
	defer leader.Close()

	mig := &captureMigrator{ch: make(chan migrateCall, 1),
		err: &service.NotLeaderError{ShardID: 1, LeaderID: 3, LeaderAddress: "10.0.0.3:63001"}}
	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.3:63001", leader.URL)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithBarrierService(&mockBarrierService{}), WithEncryptionMigrator(mig), WithLeaderForwarding(reg))

	reqBody := `{"tenant_id":"acme","strategy":"passphrase","passphrase":"cHc="}`
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/enable-encryption", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the leader's 200 relayed; body %s", rec.Code, rec.Body.String())
	}
	if got := leaderCalls.Load(); got != 1 {
		t.Fatalf("leader calls = %d, want 1", got)
	}
	if got, _ := leaderSawBody.Load().(string); got != reqBody {
		t.Fatalf("leader saw body %q, want the identical request %q", got, reqBody)
	}
	if !strings.Contains(rec.Body.String(), "migration_started") {
		t.Fatalf("follower response %q does not relay the leader's answer", rec.Body.String())
	}
}

// TestHandleBarrierEnableEncryption_NoLeaderKnownIs503 covers the election
// itself: the refusal names no leader, so there is nowhere to forward, and the
// client is told to retry (503) rather than handed a server fault.
func TestHandleBarrierEnableEncryption_NoLeaderKnownIs503(t *testing.T) {
	mig := &captureMigrator{ch: make(chan migrateCall, 1), err: &service.NotLeaderError{ShardID: 1}}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithBarrierService(&mockBarrierService{}), WithEncryptionMigrator(mig),
		WithLeaderForwarding(NewPeerAdminRegistry("63002")))

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/enable-encryption",
		strings.NewReader(`{"tenant_id":"acme","strategy":"passphrase","passphrase":"cHc="}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while no leader is known; body %s", rec.Code, rec.Body.String())
	}
}
