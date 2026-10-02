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
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// Watch method names. Watch is server-streaming and is handled by the
// dedicated streaming path in handleStream (handleWatchStream), NOT the
// request/response dispatch table — so these are intentionally absent from
// s.handlers. Authorization still applies: buildHandlers declares these
// method strings in the methodPerms registry (handleStreamOnly), so
// AuthorizeQUIC resolves them like any unary method.
const (
	watchMethodKey       = "watch.Watch"
	watchPrefixMethodKey = "watch.WatchPrefix"
)

// isWatchMethod reports whether method is a server-streaming watch method that
// must be served by handleWatchStream rather than the unary dispatch table.
func isWatchMethod(method string) bool {
	return method == watchMethodKey || method == watchPrefixMethodKey
}

// handleWatchStream serves the server-streaming watch protocol over a single
// QUIC stream: it decodes the protobuf WatchRequest, creates a watcher, and
// writes a stream of protobuf WatchResponse frames (each tagged with the
// original requestID) until the client disconnects (the stream context is
// canceled), the watcher's event channel closes, or the server is shutting
// down. The wire framing matches the SDK QUIC client's watchStream reader.
func (s *Server) handleWatchStream(ctx context.Context, stream *quicgo.Stream, requestID uint64, prefix bool, payload []byte) {
	watchSvc, svcErr := s.watchSvcFor(ctx)
	if svcErr != nil {
		statusCode, errPayload := mapServiceError(svcErr)
		_ = wire.WriteResponse(stream, requestID, statusCode, errPayload)
		return
	}

	var req pb.WatchRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		statusCode, errPayload := mapServiceError(&UnmarshalError{Err: err})
		_ = wire.WriteResponse(stream, requestID, statusCode, errPayload)
		return
	}

	// Tie the watcher's lifetime to the client stream and to the server: when
	// the client closes or resets the stream, stream.Context() is canceled,
	// and when Stop begins, s.stopping is closed; either cancels the watcher
	// and ends the stream loop, so an open watch does not hold Stop's drain
	// for its whole grace period.
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-stream.Context().Done():
			cancel()
		case <-s.stopping:
			cancel()
		case <-watchCtx.Done():
		}
	}()

	var opts []watch.WatchOption
	if req.GetStartRevision() > 0 {
		opts = append(opts, watch.WithStartRevision(req.GetStartRevision()))
	}
	if req.GetPrevKv() {
		opts = append(opts, watch.WithPrevKV())
	}
	if req.GetProgressNotify() {
		opts = append(opts, watch.WithProgressNotify())
	}

	var (
		w   watch.Watcher
		err error
	)
	if prefix || len(req.GetRangeEnd()) > 0 {
		w, err = watchSvc.WatchPrefix(watchCtx, string(req.GetKey()), opts...)
	} else {
		w, err = watchSvc.Watch(watchCtx, string(req.GetKey()), opts...)
	}
	if err != nil {
		statusCode, errPayload := mapServiceError(err)
		_ = wire.WriteResponse(stream, requestID, statusCode, errPayload)
		return
	}
	defer func() { _ = w.Close() }()

	for {
		select {
		case <-watchCtx.Done():
			s.writeWatchShutdown(stream, requestID, w.WatchID())
			return
		case resp, ok := <-w.Events():
			if !ok {
				// Channel closed: surface a watcher error if any, else end.
				if wErr := w.Err(); wErr != nil {
					statusCode, errPayload := mapServiceError(wErr)
					_ = wire.WriteResponse(stream, requestID, statusCode, errPayload)
				}
				return
			}

			events := make([]*pb.Event, len(resp.Events))
			for i, e := range resp.Events {
				events[i] = eventToProto(e)
			}
			pbResp := &pb.WatchResponse{
				WatchId:      resp.WatchID,
				Events:       events,
				Revision:     resp.Revision,
				Canceled:     resp.Canceled,
				CancelReason: resp.CancelReason,
			}
			data, mErr := proto.Marshal(pbResp)
			if mErr != nil {
				return
			}
			if wErr := wire.WriteResponse(stream, requestID, wire.StatusOK, data); wErr != nil {
				return
			}
			if resp.Canceled {
				return
			}
		}
	}
}

// watchShutdownReason is the CancelReason of the terminal frame a watch stream
// receives when the server stops.
const watchShutdownReason = "server shutting down"

// writeWatchShutdown ends a watch stream that Stop is ending with a terminal
// WatchResponse (Canceled, CancelReason watchShutdownReason), so the client
// learns why its watch stopped instead of seeing the stream end bare. It does
// nothing when the watch ended for any other reason -- the client closing the
// stream has nobody left to tell. The frame is acknowledged like a unary
// response, so it is delivered before Stop closes the connection.
func (s *Server) writeWatchShutdown(stream *quicgo.Stream, requestID uint64, watchID int64) {
	select {
	case <-s.stopping:
	default:
		return
	}
	data, err := proto.Marshal(&pb.WatchResponse{
		WatchId:      watchID,
		Canceled:     true,
		CancelReason: watchShutdownReason,
	})
	if err != nil {
		return
	}
	if err := wire.WriteResponse(stream, requestID, wire.StatusOK, data); err != nil {
		return
	}
	// Stop closes the connection once this handler returns, which would
	// discard the frame if it were still queued: wait for the client's
	// acknowledgement exactly as a unary reply does (see writeResponse).
	_ = stream.Close()
	awaitStreamAck(stream)
}
