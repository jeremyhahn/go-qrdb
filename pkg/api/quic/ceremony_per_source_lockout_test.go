// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package quic

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// TestCeremonyInitialize_PerSourceLockoutDoesNotCrossConnections is the
// go-qrdb#560 review C acceptance test for the native QUIC transport: two
// REAL, independent QUIC connections to the same server -- distinct UDP
// 4-tuples, so withConnectionState's auth.WithRemoteAddr records a distinct
// "addr:<remote>" principal for each (go-qrdb#560 review C) -- and a wrong
// SO PIN repeated past auth.SOPINFreeFailures on connection A locks out
// ONLY connection A's principal; connection B, presenting the identical
// wrong PIN immediately afterward, still gets the ordinary invalid-PIN
// refusal, never auth.SOPINLockedError's ResourceExhausted. Before
// go-qrdb#560 review C, every unauthenticated QUIC caller shared ONE
// anonymous lockout bucket, so connection B would have been locked out too,
// by a guess it never made.
func TestCeremonyInitialize_PerSourceLockoutDoesNotCrossConnections(t *testing.T) {
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{},
		WithCeremonyService(wrapped), WithCeremonyCA(ca))

	connA := env.dialServer(t)
	connB := env.dialServer(t)

	wrongPayload, err := proto.Marshal(&pb.CeremonyInitializeRequest{SoPin: "wrong-pin", AdminGenerate: true})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	// Drive connection A past the free-failure budget: SOPINFreeFailures
	// consecutive wrong guesses engage the lockout on A's own principal.
	var lastStatus uint16
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		_, lastStatus, _ = roundTrip(t, connA, "ceremony.Initialize", uint64(i+1), wrongPayload)
		if lastStatus != wire.StatusUnauthenticated {
			t.Fatalf("connection A, attempt %d: status = %d, want StatusUnauthenticated (%d)",
				i+1, lastStatus, wire.StatusUnauthenticated)
		}
	}
	// One more wrong guess on A: now locked out (ResourceExhausted), not a
	// plain invalid-PIN refusal.
	_, lockedStatus, _ := roundTrip(t, connA, "ceremony.Initialize", 1000, wrongPayload)
	if lockedStatus != wire.StatusResourceExhausted {
		t.Fatalf("connection A after %d wrong guesses: status = %d, want StatusResourceExhausted (%d)",
			auth.SOPINFreeFailures+1, lockedStatus, wire.StatusResourceExhausted)
	}

	// Connection B, a DIFFERENT source, presents the identical wrong PIN
	// immediately afterward: it must see the ordinary invalid-PIN refusal,
	// never A's lockout.
	_, statusB, _ := roundTrip(t, connB, "ceremony.Initialize", 2000, wrongPayload)
	if statusB != wire.StatusUnauthenticated {
		t.Fatalf("connection B: status = %d, want StatusUnauthenticated (%d); A's lockout leaked to B", statusB, wire.StatusUnauthenticated)
	}

	// And B can still succeed with the correct PIN while A remains locked.
	rightPayload, err := proto.Marshal(&pb.CeremonyInitializeRequest{SoPin: "correct-horse-battery", AdminGenerate: true})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	_, statusBOK, _ := roundTrip(t, connB, "ceremony.Initialize", 2001, rightPayload)
	if statusBOK != wire.StatusOK {
		t.Fatalf("connection B with the correct PIN: status = %d, want StatusOK (%d)", statusBOK, wire.StatusOK)
	}
}
