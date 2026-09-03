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

package unix

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The unix transport carries the guarded write verbs (go-qrdb#125).
//
// This assertion is the whole capability contract: transport.GuardedKVClient
// is detected by type assertion, so a consumer's `kv.(GuardedKVClient)` starts
// succeeding the moment this line compiles. It must therefore be added ONLY
// alongside methods that carry the verb's atomicity, never its name alone — a
// transport that answers the assertion and then emulates a guarded write with
// a read followed by a write is strictly worse than one that declines the
// assertion, because the consumer will build a lock on top of it.
var _ transport.GuardedKVClient = (*Transport)(nil)

// PutIfAbsent atomically stores value under key only when key is absent,
// forwarding to the server's guarded RPC. The condition is evaluated inside
// the state machine, so the answer this returns is the cluster's decision and
// not this process's guess.
//
// The unix transport is gRPC over a domain socket and talks to the SAME
// pkg/api/grpc service implementation as the TCP transport (pkg/api/unix's
// Server registers grpcpkg's service on its own listener), so the verb reaches
// service.KVServicer.PutIfAbsent by exactly the path the gRPC transport uses.
// There is nothing to emulate here and nothing is emulated: no Get precedes
// this call, and no fallback follows it.
//
// No WriteOption: a conditional write must be linearizable to mean anything,
// so it is always synchronous.
func (t *Transport) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}

	resp, err := t.client.PutIfAbsent(ctx, &pb.PutIfAbsentRequest{Key: key, Value: value})
	if err != nil {
		return false, mapGRPCError("PutIfAbsent", err)
	}
	return resp.GetStored(), nil
}

// CompareAndDelete atomically removes key only when its stored value still
// equals expected, forwarding to the server's guarded RPC. It is the release
// half of PutIfAbsent: an unguarded Delete here would let a caller that no
// longer holds a claim un-decide what the guarded create decided.
func (t *Transport) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}

	resp, err := t.client.CompareAndDelete(ctx, &pb.CompareAndDeleteRequest{Key: key, Expected: expected})
	if err != nil {
		return false, mapGRPCError("CompareAndDelete", err)
	}
	return resp.GetDeleted(), nil
}

// CompareAndSwap atomically replaces key's value with next only when its
// stored value still equals expected, forwarding to the server's guarded RPC.
// The comparison happens in the state machine, never on this side of the
// socket: emulating it with a Get and a Put would reopen precisely the window
// the verb exists to close.
func (t *Transport) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}

	resp, err := t.client.CompareAndSwap(ctx, &pb.CompareAndSwapRequest{Key: key, Expected: expected, Next: next})
	if err != nil {
		return false, mapGRPCError("CompareAndSwap", err)
	}
	return resp.GetSwapped(), nil
}
