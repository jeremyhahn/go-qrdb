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

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The QUIC transport carries the guarded write verbs (go-qrdb#124).
//
// This assertion is the whole capability contract: transport.GuardedKVClient
// is detected by type assertion, so a consumer's `kv.(GuardedKVClient)` starts
// succeeding the moment this line compiles. It must therefore be added ONLY
// alongside methods that carry the verb's atomicity, never its name alone — a
// transport that answers the assertion and then emulates a guarded write with
// a read followed by a write is strictly worse than one that declines the
// assertion, because the consumer will build a distributed lock on top of it.
//
// The three methods below satisfy that: each marshals one request, makes one
// t.call, and returns the server's answer. No Get precedes any of them and no
// read-then-write fallback follows. On the server side the matching QUIC
// handler calls service.KVServicer's atomic method directly (pkg/api/quic's
// handleKVPutIfAbsent and siblings), so the condition is evaluated inside the
// state machine exactly as it is for the embedded and gRPC transports.
var _ transport.GuardedKVClient = (*Transport)(nil)

// PutIfAbsent atomically stores value under key only when key is absent,
// returning whether it stored. It is the acquire half of a claim.
//
// No WriteOption: a conditional write must be linearizable to mean anything,
// so it is always synchronous — a relaxed-durability guarded write would be a
// guarantee it cannot keep. PutIfAbsentRequest carries no WriteMode field for
// the same reason.
func (t *Transport) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.PutIfAbsentRequest{Key: key, Value: value})
	if err != nil {
		return false, &transport.RequestError{Method: "PutIfAbsent", Err: err}
	}

	respPayload, err := t.call(ctx, "kv.PutIfAbsent", payload)
	if err != nil {
		return false, err
	}

	var resp pb.PutIfAbsentResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return false, &transport.RequestError{Method: "PutIfAbsent", Err: err}
	}
	return resp.GetStored(), nil
}

// CompareAndDelete atomically removes key only when its stored value still
// equals expected, returning whether it deleted. It is the release half of
// PutIfAbsent: an unguarded Delete here would let a caller that no longer
// holds a claim un-decide what the guarded create decided.
func (t *Transport) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.CompareAndDeleteRequest{Key: key, Expected: expected})
	if err != nil {
		return false, &transport.RequestError{Method: "CompareAndDelete", Err: err}
	}

	respPayload, err := t.call(ctx, "kv.CompareAndDelete", payload)
	if err != nil {
		return false, err
	}

	var resp pb.CompareAndDeleteResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return false, &transport.RequestError{Method: "CompareAndDelete", Err: err}
	}
	return resp.GetDeleted(), nil
}

// CompareAndSwap atomically replaces key's value with next only when its
// stored value still equals expected, returning whether it replaced. An absent
// key is left absent — a swap is not a create.
//
// The comparison happens in the state machine, never on this side of the wire:
// emulating it with a Get and a Put would reopen the window the verb exists to
// close, and would do so across a network round trip.
func (t *Transport) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.CompareAndSwapRequest{Key: key, Expected: expected, Next: next})
	if err != nil {
		return false, &transport.RequestError{Method: "CompareAndSwap", Err: err}
	}

	respPayload, err := t.call(ctx, "kv.CompareAndSwap", payload)
	if err != nil {
		return false, err
	}

	var resp pb.CompareAndSwapResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return false, &transport.RequestError{Method: "CompareAndSwap", Err: err}
	}
	return resp.GetSwapped(), nil
}
