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

// Watch handles the bidirectional Watch stream. Each stream can
// create/cancel multiple watches identified by watch IDs.
func (s *watchServer) Watch(stream pb.Watch_WatchServer) error {
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
				if sendErr := stream.Send(&pb.WatchResponse{
					Header:       responseHeader(s.clusterID, s.memberID, 0, 0),
					Created:      false,
					Canceled:     true,
					CancelReason: "key is required",
				}); sendErr != nil {
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
				if sendErr := stream.Send(&pb.WatchResponse{
					Header:       responseHeader(s.clusterID, s.memberID, 0, 0),
					Created:      false,
					Canceled:     true,
					CancelReason: watchErr.Error(),
				}); sendErr != nil {
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

// forwardEvents reads from a qrdb Watcher and sends etcd WatchResponses
// on the stream. The goroutine exits when the watcher's event channel
// is closed or a send error occurs.
func (s *watchServer) forwardEvents(stream pb.Watch_WatchServer, id int64, watcher watch.Watcher) {
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
