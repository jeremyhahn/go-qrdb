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

package etcd

import (
	"errors"
	"sync"
	"sync/atomic"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// watchServer implements etcdserverpb.WatchServer by delegating to
// qrdb's WatchServicer with per-stream watch ID multiplexing.
type watchServer struct {
	pb.UnimplementedWatchServer
	watchSvc  service.WatchServicer
	mvccSvc   service.MVCCKVServicer
	clusterID uint64
	memberID  uint64
}

// serialWatchStream serializes Send on one Watch stream. gRPC forbids
// concurrent sends on a stream, and a stream's responses come from its
// request loop and from one forwarding goroutine per watch.
type serialWatchStream struct {
	pb.Watch_WatchServer
	mu sync.Mutex
}

// Send sends resp, one caller at a time.
func (s *serialWatchStream) Send(resp *pb.WatchResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Watch_WatchServer.Send(resp)
}

// invalidWatchID is the watch ID etcd answers a failed create with.
const invalidWatchID = -1

// Watch handles the bidirectional Watch stream. Each stream can
// create/cancel multiple watches identified by watch IDs.
func (s *watchServer) Watch(raw pb.Watch_WatchServer) error {
	stream := &serialWatchStream{Watch_WatchServer: raw}
	ctx := stream.Context()

	var (
		mu       sync.Mutex
		watchers = make(map[int64]watch.Watcher) // watchID -> Watcher
		nextID   atomic.Int64
	)
	nextID.Store(1)

	// Cleanup all watchers on stream close.
	defer func() {
		mu.Lock()
		for _, w := range watchers {
			_ = w.Close()
		}
		mu.Unlock()
	}()

	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}

		switch uv := req.RequestUnion.(type) {
		case *pb.WatchRequest_CreateRequest:
			cr := uv.CreateRequest
			if len(cr.Key) == 0 {
				if sendErr := s.failCreate(stream, "key is required"); sendErr != nil {
					return sendErr
				}
				continue
			}

			watchID := cr.WatchId
			if watchID == 0 {
				watchID = nextID.Add(1) - 1
			}

			var watchOpts []watch.WatchOption
			if cr.StartRevision > 0 {
				watchOpts = append(watchOpts, watch.WithStartRevision(cr.StartRevision))
			}

			var w watch.Watcher
			var watchErr error
			if len(cr.RangeEnd) > 0 {
				// Prefix/range watch.
				w, watchErr = s.watchSvc.WatchPrefix(ctx, string(cr.Key), watchOpts...)
			} else {
				w, watchErr = s.watchSvc.Watch(ctx, string(cr.Key), watchOpts...)
			}
			if watchErr != nil {
				if sendErr := s.refuseCreate(stream, watchID, watchErr); sendErr != nil {
					return sendErr
				}
				continue
			}

			mu.Lock()
			watchers[watchID] = w
			mu.Unlock()

			// Send created response.
			if sendErr := stream.Send(&pb.WatchResponse{
				Header:  responseHeader(s.clusterID, s.memberID, 0, 0),
				WatchId: watchID,
				Created: true,
			}); sendErr != nil {
				return sendErr
			}

			// Start goroutine to forward events.
			go s.forwardEvents(stream, watchID, w)

		case *pb.WatchRequest_CancelRequest:
			cancelID := uv.CancelRequest.WatchId
			mu.Lock()
			if w, ok := watchers[cancelID]; ok {
				_ = w.Close()
				delete(watchers, cancelID)
			}
			mu.Unlock()

			if sendErr := stream.Send(&pb.WatchResponse{
				Header:   responseHeader(s.clusterID, s.memberID, 0, 0),
				WatchId:  cancelID,
				Canceled: true,
			}); sendErr != nil {
				return sendErr
			}
		}
	}
}

// refuseCreate answers a create request the watch service refused, in etcd's
// shapes. A start revision at or below the compaction floor is etcd's
// compacted watch: the watch is created, then cancelled by a response
// carrying the floor as its CompactRevision, which an etcd client reports as
// ErrCompacted. Any other refusal is etcd's failed create: Created and
// Canceled with the reason and no watch ID, which an etcd client reports as
// the watch's error.
func (s *watchServer) refuseCreate(stream pb.Watch_WatchServer, watchID int64, watchErr error) error {
	if compacted, ok := compactedResponse(s.clusterID, s.memberID, watchID, watchErr); ok {
		if err := stream.Send(&pb.WatchResponse{
			Header:  responseHeader(s.clusterID, s.memberID, 0, 0),
			WatchId: watchID,
			Created: true,
		}); err != nil {
			return err
		}
		return stream.Send(compacted)
	}
	return s.failCreate(stream, watchErr.Error())
}

// failCreate answers a create request with etcd's failed create: Created and
// Canceled, with no watch ID and the reason. An etcd client matches it to its
// pending create and reports the reason as the watch's error; a response
// without Created matches no pending create, and the client waits forever.
func (s *watchServer) failCreate(stream pb.Watch_WatchServer, reason string) error {
	return stream.Send(&pb.WatchResponse{
		Header:       responseHeader(s.clusterID, s.memberID, 0, 0),
		WatchId:      invalidWatchID,
		Created:      true,
		Canceled:     true,
		CancelReason: reason,
	})
}

// compactedResponse builds etcd's compacted-watch response for watch id when
// err is a compaction refusal carrying a floor, and reports false otherwise.
func compactedResponse(clusterID, memberID uint64, id int64, err error) (*pb.WatchResponse, bool) {
	var compacted *watch.CompactedError
	if !errors.As(err, &compacted) || compacted.CompactRevision <= 0 {
		return nil, false
	}
	return &pb.WatchResponse{
		Header:          responseHeader(clusterID, memberID, 0, 0),
		WatchId:         id,
		CompactRevision: compacted.CompactRevision,
		Canceled:        true,
	}, true
}

// forwardEvents reads from a qrdb Watcher and sends etcd WatchResponses
// on the stream. The goroutine exits when the watcher's event channel
// is closed or a send error occurs. A watch that ends for a reason -- the
// compaction floor outrunning its replay, an overflowed consumer -- is
// cancelled on the stream so the client learns it ended: with the floor as
// CompactRevision for a compaction, else with the reason. A watch closed
// cleanly (a cancel request, the stream ending) sends nothing here.
func (s *watchServer) forwardEvents(stream pb.Watch_WatchServer, id int64, watcher watch.Watcher) {
	defer func() {
		err := watcher.Err()
		if err == nil {
			return
		}
		if compacted, ok := compactedResponse(s.clusterID, s.memberID, id, err); ok {
			_ = stream.Send(compacted)
			return
		}
		_ = stream.Send(&pb.WatchResponse{
			Header:       responseHeader(s.clusterID, s.memberID, 0, 0),
			WatchId:      id,
			Canceled:     true,
			CancelReason: err.Error(),
		})
	}()

	for resp := range watcher.Events() {
		etcdEvents := make([]*mvccpb.Event, 0, len(resp.Events))
		for _, evt := range resp.Events {
			if evt == nil {
				continue
			}
			evtType := mvccpb.PUT
			if evt.Type == watch.EventDelete {
				evtType = mvccpb.DELETE
			}
			etcdEvt := &mvccpb.Event{
				Type: evtType,
				Kv:   kvToEtcd(evt.Kv),
			}
			if evt.PrevKv != nil {
				etcdEvt.PrevKv = kvToEtcd(evt.PrevKv)
			}
			etcdEvents = append(etcdEvents, etcdEvt)
		}

		if sendErr := stream.Send(&pb.WatchResponse{
			Header:  responseHeader(s.clusterID, s.memberID, resp.Revision, 0),
			WatchId: id,
			Events:  etcdEvents,
		}); sendErr != nil {
			return
		}
	}
}
