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
	"sync/atomic"

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
	if t.send != nil {
		return nil, errFrameStream
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

	// The write side stays open until readWatchResponses is done reading:
	// under wire.ProtocolV3 the client's FIN acknowledges what it has read,
	// and the server waits for it after the terminal frame it sends when it
	// stops, so that frame is delivered rather than lost to the connection
	// closing behind it.

	// Tie the stream's read side to ctx: OpenStreamSync only used ctx to
	// establish the stream, so without this a caller cancellation (Ctrl-C,
	// --timeout) would not unblock a readWatchResponses goroutine parked in
	// wire.ReadResponse waiting on the server. CancelRead is idempotent with
	// the deferred CancelRead(0) in readWatchResponses on normal exit, and
	// this goroutine exits as soon as either happens.
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stream.CancelRead(0)
		case <-watchDone:
		}
	}()

	ch := make(chan transport.WatchResponse, 16)
	go func() {
		readWatchResponses(ctx, stream, ch)
		close(watchDone)
	}()

	return ch, nil
}

// beforeWatchRead is a test hook: when set (only from _test.go files),
// readWatchResponses calls it after its ctx.Done() check and just before
// the blocking wire.ReadResponse, so a test can cancel while the reader is
// committed to the read. It receives the watch's context so a test can
// act only for its own watch: reader goroutines of earlier watches may
// still be running when a test sets it, and their reads must not trip the
// test's barrier. Atomic for the same reason.
var beforeWatchRead atomic.Pointer[func(ctx context.Context)]

// readWatchResponses reads wire.ReadResponse frames from the given
// stream in a loop, decodes them as protobuf WatchResponse messages,
// and sends them to the channel. The loop terminates when the context
// is canceled, the stream is closed, or a read error occurs. The
// channel is always closed on exit.
func readWatchResponses(ctx context.Context, stream *quicgo.Stream, ch chan<- transport.WatchResponse) {
	defer close(ch)
	defer stream.CancelRead(0)
	// Runs first: the FIN that acknowledges every frame read so far.
	defer func() { _ = stream.Close() }()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if hook := beforeWatchRead.Load(); hook != nil {
			(*hook)(ctx)
		}
		_, statusCode, payload, err := wire.ReadResponse(stream)
		if err != nil {
			return
		}

		if statusCode != wire.StatusOK {
			reason := watchCancelReason(payload)
			if statusCode == wire.StatusUnimplemented {
				reason = transport.UnsupportedWatchReason(reason)
			}
			ch <- transport.WatchResponse{
				Canceled:     true,
				CancelReason: reason,
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

// watchCancelReason is the reason a watch error frame gives: the message of
// its wire.ErrorPayload body, or, for a body that does not decode, that
// decoding failure -- never the undecoded bytes.
func watchCancelReason(payload []byte) string {
	decoded, err := wire.DecodeErrorPayload(payload)
	if err != nil {
		return err.Error()
	}
	return decoded.Message
}
