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

package server

import (
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The ReadIndex warm-up of a freshly elected leader is waited out; any
// other failure, and a cancelled context, is returned at once.
func TestWhileSystemShardSettling(t *testing.T) {
	calls := 0
	err := WhileSystemShardSettling(context.Background(), func() error {
		calls++
		if calls < 3 {
			return quicraft.ErrReadIndexNotReady
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err = %v after %d calls, want nil after 3", err, calls)
	}

	boom := errors.New("boom")
	calls = 0
	err = WhileSystemShardSettling(context.Background(), func() error { calls++; return boom })
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want boom after 1", err, calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	err = WhileSystemShardSettling(ctx, func() error { calls++; return nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("err = %v after %d calls, want context.Canceled before any call", err, calls)
	}
}

// An election in progress -- the system shard has no known leader -- is
// waited out the same way. A node booting while a peer restarts (a rolling
// enable-encryption recycles every node in turn) loses the leader it found
// during provisioning and must not fail on the read that follows. A
// not-leader answer that names a leader is not an election and is returned.
func TestWhileSystemShardSettling_WaitsOutAnElection(t *testing.T) {
	calls := 0
	err := WhileSystemShardSettling(context.Background(), func() error {
		calls++
		if calls < 3 {
			return &service.NotLeaderError{ShardID: 1}
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err = %v after %d calls, want nil after 3", err, calls)
	}

	known := &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "192.0.2.2:5000"}
	calls = 0
	err = WhileSystemShardSettling(context.Background(), func() error { calls++; return known })
	if !errors.Is(err, known) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want the known-leader error after 1", err, calls)
	}
}

// TestWhileSystemShardSettling_WaitsOutAnElection_TransportLayer is the
// embedded-transport proof: a caller one layer removed from the service
// package (the feature gate's self-advertisement, writing through an
// in-process sdk.Client) sees *transport.NotLeaderError, not
// *service.NotLeaderError -- sdk/go/transport/embedded's translateError
// reconstructs a fresh value with no Unwrap chain back to the service-layer
// type -- and must be waited out exactly the same way.
func TestWhileSystemShardSettling_WaitsOutAnElection_TransportLayer(t *testing.T) {
	calls := 0
	err := WhileSystemShardSettling(context.Background(), func() error {
		calls++
		if calls < 3 {
			return &transport.NotLeaderError{ShardID: 1}
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err = %v after %d calls, want nil after 3", err, calls)
	}

	known := &transport.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "192.0.2.2:5000"}
	calls = 0
	err = WhileSystemShardSettling(context.Background(), func() error { calls++; return known })
	if !errors.Is(err, known) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want the known-leader error after 1", err, calls)
	}
}
