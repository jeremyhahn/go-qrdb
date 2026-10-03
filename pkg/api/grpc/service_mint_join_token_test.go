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

package grpc

import (
	"context"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// mintJoinTokenAdmin is an admin double implementing
// service.ClusterJoinTokenMinter, recording the ttl it was called with so
// a test can prove whether the RPC handler called it at all.
type mintJoinTokenAdmin struct {
	*mockAdminService
	called  bool
	gotTTL  time.Duration
	token   string
	expires time.Time
	err     error
}

func (a *mintJoinTokenAdmin) MintJoinToken(_ context.Context, _ string, ttl time.Duration) (string, time.Time, error) {
	a.called = true
	a.gotTTL = ttl
	return a.token, a.expires, a.err
}

// TestGRPC_MintJoinToken_TTLOverflowRefused proves a ttl_seconds value
// large enough to overflow the seconds-to-duration conversion is refused
// BEFORE the RPC handler ever calls the minter.
func TestGRPC_MintJoinToken_TTLOverflowRefused(t *testing.T) {
	admin := &mintJoinTokenAdmin{mockAdminService: newMockAdminService(), token: "tok", expires: time.Now().Add(time.Hour)}
	s := &qrdbServiceServer{adminSvc: admin}

	_, err := s.MintJoinToken(context.Background(), &pb.MintJoinTokenRequest{NodeId: "node-1", TtlSeconds: 9223372036854775807})
	if err == nil {
		t.Fatal("MintJoinToken with an overflowing ttl_seconds succeeded, want a refusal")
	}
	if admin.called {
		t.Error("minter was called with an overflowing ttl_seconds; the guard did not refuse it first")
	}
}

// TestGRPC_MintJoinToken_OrdinaryTTLConverted proves an ordinary
// ttl_seconds value converts to the equivalent time.Duration and reaches
// the minter.
func TestGRPC_MintJoinToken_OrdinaryTTLConverted(t *testing.T) {
	admin := &mintJoinTokenAdmin{mockAdminService: newMockAdminService(), token: "tok", expires: time.Now().Add(time.Hour)}
	s := &qrdbServiceServer{adminSvc: admin}

	resp, err := s.MintJoinToken(context.Background(), &pb.MintJoinTokenRequest{NodeId: "node-1", TtlSeconds: 3600})
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	if !admin.called || admin.gotTTL != time.Hour {
		t.Fatalf("minter called=%v ttl=%v, want called with 1h", admin.called, admin.gotTTL)
	}
	if resp.GetToken() != "tok" {
		t.Fatalf("token = %q, want tok", resp.GetToken())
	}
}
