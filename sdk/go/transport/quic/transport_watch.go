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

	quicgo "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.WatchClient = (*Transport)(nil)

// Watch creates a watcher that delivers events for the specified key
// through the QUIC transport. It opens a dedicated QUIC stream for
// server-streaming watch responses. The returned channel is closed
// when the context is canceled or the stream ends.
func (t *Transport) Watch(ctx context.Context, key string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return t.watchStream(ctx, "watch.Watch", []byte(key), nil, opts)
}

// WatchPrefix creates a watcher that delivers events for all keys
// matching the given prefix through the QUIC transport. It opens a
// dedicated QUIC stream for server-streaming watch responses. The
// returned channel is closed when the context is canceled or the
// stream ends.
func (t *Transport) WatchPrefix(ctx context.Context, prefix string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return t.watchStream(ctx, "watch.WatchPrefix", []byte(prefix), nil, opts)
}

// watchStream opens a QUIC stream, writes a WatchRequest frame, and
// spawns a goroutine that reads WatchResponse frames in a loop,
// delivering decoded events to the returned channel. Context
// cancellation closes the stream and the channel.
func (t *Transport) watchStream(ctx context.Context, method string, key, rangeEnd []byte, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.WatchRequest{
		Key:            key,
		RangeEnd:       rangeEnd,
		StartRevision:  opts.StartRevision,
		PrevKv:         opts.PrevKV,
		ProgressNotify: opts.ProgressNotify,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	stream, err := t.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, &streamError{Err: err}
	}

	requestID := t.requestID.Add(1)
	if err := wire.WriteRequest(stream, method, t.database, requestID, payload); err != nil {
		stream.CancelWrite(0)
		stream.CancelRead(0)
		return nil, &streamError{Err: err}
	}

	// Close the write side (send FIN) to signal the server that the
	// request frame is complete while keeping the read side open for
	// streaming responses.
	if err := stream.Close(); err != nil {
		stream.CancelRead(0)
		return nil, &streamError{Err: err}
	}

	ch := make(chan transport.WatchResponse, 16)
	go readWatchResponses(ctx, stream, ch)

	return ch, nil
}

// readWatchResponses reads wire.ReadResponse frames from the given
// stream in a loop, decodes them as protobuf WatchResponse messages,
// and sends them to the channel. The loop terminates when the context
// is canceled, the stream is closed, or a read error occurs. The
// channel is always closed on exit.
func readWatchResponses(ctx context.Context, stream *quicgo.Stream, ch chan<- transport.WatchResponse) {
	defer close(ch)
	defer stream.CancelRead(0)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_, statusCode, payload, err := wire.ReadResponse(stream)
		if err != nil {
			return
		}

		if statusCode != wire.StatusOK {
			ch <- transport.WatchResponse{
				Canceled:     true,
				CancelReason: string(payload),
			}
			return
		}

		var pbResp pb.WatchResponse
		if err := proto.Unmarshal(payload, &pbResp); err != nil {
			ch <- transport.WatchResponse{
				Canceled:     true,
				CancelReason: (&unmarshalError{Err: err}).Error(),
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

// protoEventTypeToTransport converts proto EventType to transport EventType.
var protoEventTypeToTransport = map[pb.EventType]transport.EventType{
	pb.EventType_EVENT_PUT:    transport.EventPut,
	pb.EventType_EVENT_DELETE: transport.EventDelete,
}

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
