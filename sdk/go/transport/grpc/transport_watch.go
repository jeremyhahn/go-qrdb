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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.WatchClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Enum conversion maps
// ---------------------------------------------------------------------------

// protoEventTypeToSDK converts proto EventType to transport EventType.
var protoEventTypeToSDK = map[pb.EventType]transport.EventType{
	pb.EventType_EVENT_PUT:    transport.EventPut,
	pb.EventType_EVENT_DELETE: transport.EventDelete,
}

// ---------------------------------------------------------------------------
// WatchClient implementation
// ---------------------------------------------------------------------------

// Watch creates a watcher that delivers events for the specified key
// through the gRPC transport. It opens a server-streaming RPC and
// spawns a goroutine that reads responses into the returned channel.
// The caller must cancel the context to stop watching.
func (t *Transport) Watch(ctx context.Context, key string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return t.watchStream(ctx, []byte(key), nil, opts)
}

// WatchPrefix creates a watcher that delivers events for all keys
// matching the given prefix through the gRPC transport. It encodes
// the prefix as a range_end using the prefix-end sentinel (the prefix
// with the last byte incremented) per standard practice.
func (t *Transport) WatchPrefix(ctx context.Context, prefix string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return t.watchStream(ctx, []byte(prefix), prefixRangeEnd([]byte(prefix)), opts)
}

// watchStream opens a gRPC server-streaming Watch RPC and spawns a
// goroutine that reads WatchResponse messages in a loop, delivering
// decoded events to the returned channel. Context cancellation closes
// the stream and the channel.
func (t *Transport) watchStream(ctx context.Context, key, rangeEnd []byte, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	stream, err := t.client.Watch(ctx, &pb.WatchRequest{
		Key:            key,
		RangeEnd:       rangeEnd,
		StartRevision:  opts.StartRevision,
		PrevKv:         opts.PrevKV,
		ProgressNotify: opts.ProgressNotify,
	})
	if err != nil {
		return nil, mapGRPCError("Watch", err)
	}

	ch := make(chan transport.WatchResponse, 64)
	go readGRPCWatchResponses(ctx, stream, ch)

	return ch, nil
}

// readGRPCWatchResponses reads WatchResponse messages from the gRPC
// server stream in a loop, converts them to transport types, and sends
// them to the channel. The loop terminates when the context is canceled,
// the stream is closed, or a read error occurs. The channel is always
// closed on exit.
func readGRPCWatchResponses(ctx context.Context, stream pb.QRDBService_WatchClient, ch chan<- transport.WatchResponse) {
	defer close(ch)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		pbResp, err := stream.Recv()
		if err != nil {
			return
		}

		events := make([]*transport.WatchEvent, len(pbResp.GetEvents()))
		for i, ev := range pbResp.GetEvents() {
			events[i] = protoToWatchEvent(ev)
		}

		resp := transport.WatchResponse{
			WatchID:      pbResp.GetWatchId(),
			Events:       events,
			Canceled:     pbResp.GetCanceled(),
			CancelReason: pbResp.GetCancelReason(),
			Revision:     pbResp.GetRevision(),
		}

		select {
		case <-ctx.Done():
			return
		case ch <- resp:
		}

		if pbResp.GetCanceled() {
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// protoToWatchEvent converts a proto Event to a transport WatchEvent.
func protoToWatchEvent(ev *pb.Event) *transport.WatchEvent {
	if ev == nil {
		return nil
	}
	return &transport.WatchEvent{
		Type:   protoEventTypeToSDK[ev.GetType()],
		Kv:     protoToMVCCKV(ev.GetKv()),
		PrevKv: protoToMVCCKV(ev.GetPrevKv()),
	}
}

// prefixRangeEnd computes the end key for a prefix watch by incrementing
// the last byte of the prefix. This creates a half-open range [prefix,
// prefixEnd) that matches all keys with the given prefix. Returns nil
// for empty prefixes.
func prefixRangeEnd(prefix []byte) []byte {
	if len(prefix) == 0 {
		return nil
	}
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	// All bytes are 0xff; return a zero byte to indicate no upper bound.
	return []byte{0}
}
