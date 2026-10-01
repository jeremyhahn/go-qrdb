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
	"errors"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.WatchClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Proto conversion maps
// ---------------------------------------------------------------------------

// protoEventTypeToTransport converts proto EventType to transport EventType.
var protoEventTypeToTransport = map[pb.EventType]transport.EventType{
	pb.EventType_EVENT_PUT:    transport.EventPut,
	pb.EventType_EVENT_DELETE: transport.EventDelete,
}

// ---------------------------------------------------------------------------
// WatchClient implementation
// ---------------------------------------------------------------------------

// Watch creates a watcher that delivers events for the specified key.
// Returns a channel of WatchResponse that is closed when the watch
// ends. The caller must cancel the context to stop watching.
func (t *Transport) Watch(ctx context.Context, key string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return t.watchStream(ctx, []byte(key), nil, opts)
}

// WatchPrefix creates a watcher that delivers events for all keys
// matching the given prefix. The prefix is used as the key with a
// range end to capture all keys sharing the prefix.
func (t *Transport) WatchPrefix(ctx context.Context, prefix string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	rangeEnd := prefixRangeEnd([]byte(prefix))
	return t.watchStream(ctx, []byte(prefix), rangeEnd, opts)
}

// watchStream opens a gRPC server-streaming Watch RPC and spawns a
// goroutine that reads WatchResponse messages from the stream,
// converting them to SDK types and delivering to the returned channel.
// The channel is closed when the context is canceled, the stream ends,
// or an error occurs.
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
		return nil, transport.ClassifyWatchError(mapGRPCError("Watch", err))
	}

	ch := make(chan transport.WatchResponse, 16)
	go readGRPCWatchResponses(ctx, stream, ch)

	return ch, nil
}

// readGRPCWatchResponses reads WatchResponse messages from the gRPC
// server stream and sends them to the channel. The loop terminates
// when the context is canceled, the stream is closed, or a read error
// occurs. The channel is always closed on exit.
//
// A server-side watch failure (e.g. a compacted start revision, see
// mapServiceError) ends the RPC with a gRPC status error rather than a final
// WatchResponse frame — that is delivered to Recv(), not sent as a message.
// Recv() also returns an error for a normal, healthy stream end (io.EOF) and
// for caller-initiated cancellation (ctx.Done()). Only the first case is a
// server-reported watch failure worth surfacing to the caller as a terminal
// Canceled response; the other two are silent, expected stream ends.
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
			if ctx.Err() != nil || errors.Is(err, io.EOF) {
				return
			}
			// A context-deadline/cancel driven teardown (the CLI's --timeout or
			// Ctrl-C) surfaces here as a gRPC Canceled/DeadlineExceeded status on
			// a timing path where ctx.Err() may not yet be observable in this
			// goroutine. Treat it as the same silent, expected local shutdown
			// rather than leaking the transport's RST_STREAM text into the
			// caller's watch output. A genuine server-initiated terminal error
			// (compacted → OutOfRange, unsupported → Unimplemented, overflow, …)
			// carries a different code and still surfaces as a Canceled response.
			if code := status.Code(err); code == codes.Canceled || code == codes.DeadlineExceeded {
				return
			}
			reason := err.Error()
			if st, ok := status.FromError(err); ok {
				reason = st.Message()
				if st.Code() == codes.Unimplemented {
					reason = transport.UnsupportedWatchReason(reason)
				}
			}
			select {
			case ch <- transport.WatchResponse{Canceled: true, CancelReason: reason}:
			case <-ctx.Done():
			}
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
// Proto conversion helpers
// ---------------------------------------------------------------------------

// protoToWatchEvent converts a proto Event to a transport WatchEvent.
func protoToWatchEvent(ev *pb.Event) *transport.WatchEvent {
	if ev == nil {
		return nil
	}
	return &transport.WatchEvent{
		Type:   protoEventTypeToTransport[ev.GetType()],
		Kv:     protoToMVCCKeyValue(ev.GetKv()),
		PrevKv: protoToMVCCKeyValue(ev.GetPrevKv()),
	}
}

// prefixRangeEnd computes the range end for a prefix watch. Given a
// prefix like "foo", it returns "fop" (the prefix with its last byte
// incremented). This produces the half-open range [prefix, rangeEnd)
// that captures all keys sharing the prefix.
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
	// All 0xff bytes: no upper bound (watch all keys).
	return nil
}
