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
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_TransferLeadership_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.TransferLeadership", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.TransferLeadership(context.Background(), 1, 3); err != nil {
		t.Fatalf("TransferLeadership: %v", err)
	}
	rec := srv.LastRequest("admin.TransferLeadership")
	if rec == nil {
		t.Fatal("no recorded admin.TransferLeadership request")
	}
	var r pb.TransferLeadershipRequest
	if err := proto.Unmarshal(rec.Payload, &r); err != nil {
		t.Fatalf("unmarshal recorded request: %v", err)
	}
	if r.GetShardId() != 1 || r.GetReplicaId() != 3 {
		t.Fatalf("request = shard %d replica %d, want shard 1 replica 3", r.GetShardId(), r.GetReplicaId())
	}
}

func TestE2E_Transport_TransferLeadership_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.TransferLeadership", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("transfer failed")
	})
	assertQuicRequestError(t, tr.TransferLeadership(context.Background(), 1, 3))
}

func TestTransport_TransferLeadership_Closed(t *testing.T) {
	_, tr := startE2EServer(t)
	_ = tr.Close()

	err := tr.TransferLeadership(context.Background(), 1, 3)
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("err = %v (%T), want *transport.ClosedError", err, err)
	}
}

// TestMapWireError_CarriesTheLeaderTransferRefusal: an error frame whose
// payload carries the qrdb.v1 LEADER_TRANSFER_TIMEOUT ErrorInfo reaches the
// caller as a RequestError whose Reason and Metadata rebuild the typed timeout;
// an ErrorInfo from another domain is not read.
func TestMapWireError_CarriesTheLeaderTransferRefusal(t *testing.T) {
	want := &service.LeaderTransferTimeoutError{ShardID: 1, FromReplicaID: 1, ToReplicaID: 3, Timeout: 5 * time.Second}
	reason, md, ok := replicaforward.Refusal(want)
	if !ok {
		t.Fatal("the timeout is not a refusal")
	}
	encode := func(domain string) []byte {
		body, err := wire.EncodeErrorPayload(wire.StatusUnavailable, wire.ErrorPayload{
			Message: want.Error(),
			Info:    &wire.ErrorInfo{Domain: domain, Reason: reason, Metadata: md},
		})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}

	var reqErr *transport.RequestError
	if got := mapWireError("admin.TransferLeadership", wire.StatusUnavailable, encode(transport.RefusalDomain)); !errors.As(got, &reqErr) {
		t.Fatalf("mapped = %v (%T), want *transport.RequestError", got, got)
	}
	if reqErr.Reason != replicaforward.ReasonLeaderTransferTimeout {
		t.Fatalf("reason = %q, want %s", reqErr.Reason, replicaforward.ReasonLeaderTransferTimeout)
	}
	rebuilt, err := replicaforward.ParseLeaderTransferTimeout(reqErr.Metadata)
	if err != nil {
		t.Fatalf("ParseLeaderTransferTimeout: %v", err)
	}
	if *rebuilt != *want {
		t.Fatalf("rebuilt %+v, want %+v", *rebuilt, *want)
	}

	if got := mapWireError("admin.TransferLeadership", wire.StatusUnavailable, encode("other.v1")); !errors.As(got, &reqErr) || reqErr.Reason != "" {
		t.Fatalf("mapped = %v, want a RequestError with no reason for a foreign domain", got)
	}
}
