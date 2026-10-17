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

package sdk_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// transferReturning is a ClusterClient whose transport answers every
// TransferLeadership with err.
func transferReturning(t *testing.T, err error) *sdk.ClusterClient {
	t.Helper()
	ft := &fakeLeaderTransferTransport{
		fakeClusterTransport: &fakeClusterTransport{},
		transfer:             func(context.Context, uint64, uint64) error { return err },
	}
	client, newErr := sdk.NewClient(ft)
	if newErr != nil {
		t.Fatalf("NewClient: %v", newErr)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client.Cluster()
}

// TestClusterClient_TransferLeadership_RebuildsTheWireTimeout: a wire
// transport's LEADER_TRANSFER_TIMEOUT refusal reaches the caller as the typed
// *LeaderTransferTimeoutError, field for field.
func TestClusterClient_TransferLeadership_RebuildsTheWireTimeout(t *testing.T) {
	want := &sdk.LeaderTransferTimeoutError{ShardID: 3, FromReplicaID: 1, ToReplicaID: 7, Timeout: 5 * time.Second}
	reason, md, ok := replicaforward.Refusal(want)
	if !ok {
		t.Fatal("the timeout is not a refusal")
	}
	cc := transferReturning(t, &transport.RequestError{StatusCode: 503, Reason: reason, Metadata: md})

	var lte *sdk.LeaderTransferTimeoutError
	if err := cc.TransferLeadership(context.Background(), 3, 7); !errors.As(err, &lte) {
		t.Fatalf("got %T (%v), want *LeaderTransferTimeoutError", err, err)
	}
	if *lte != *want {
		t.Fatalf("rebuilt %+v, want %+v", *lte, *want)
	}
}

// TestClusterClient_TransferLeadership_KeepsOtherErrors: a refusal whose
// metadata does not decode, a RequestError with another reason, and an error
// that is no RequestError all pass through unchanged.
func TestClusterClient_TransferLeadership_KeepsOtherErrors(t *testing.T) {
	for name, sent := range map[string]error{
		"malformed metadata": &transport.RequestError{Reason: replicaforward.ReasonLeaderTransferTimeout, Metadata: map[string]string{"shard": "x"}},
		"other reason":       &transport.RequestError{Reason: "NOT_LEADER"},
		"not a RequestError": &transport.ClosedError{},
	} {
		t.Run(name, func(t *testing.T) {
			cc := transferReturning(t, sent)
			err := cc.TransferLeadership(context.Background(), 3, 7)
			if err != sent {
				t.Fatalf("got %T (%v), want the transport's error unchanged", err, err)
			}
			var lte *sdk.LeaderTransferTimeoutError
			if errors.As(err, &lte) {
				t.Fatal("a typed timeout was invented")
			}
		})
	}
}
