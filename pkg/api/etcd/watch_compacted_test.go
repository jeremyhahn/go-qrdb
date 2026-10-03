//go:build qrdb_etcd

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
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// wrappedWatchError wraps a watch error the way a service layer does, so the
// listener is shown to find the floor through errors.As, not by identity.
type wrappedWatchError struct{ cause error }

func (e *wrappedWatchError) Error() string { return "watch create: " + e.cause.Error() }
func (e *wrappedWatchError) Unwrap() error { return e.cause }

// refusingWatchServicer refuses every watch with err.
func refusingWatchServicer(err error) *mockWatchServicer {
	refuse := func(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) { return nil, err }
	return &mockWatchServicer{watchFn: refuse, watchPrefixFn: refuse}
}

// servingWatchServicer serves every watch with w.
func servingWatchServicer(w watch.Watcher) *mockWatchServicer {
	serve := func(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) { return w, nil }
	return &mockWatchServicer{watchFn: serve, watchPrefixFn: serve}
}

// runCreate drives one create request through srv on a recorded stream and
// returns what was sent.
func runCreate(t *testing.T, srv *watchServer, create *pb.WatchCreateRequest) []*pb.WatchResponse {
	t.Helper()
	stream := newMockWatchStream(context.Background())
	stream.recvCh <- &pb.WatchRequest{RequestUnion: &pb.WatchRequest_CreateRequest{CreateRequest: create}}
	stream.recvErr = io.EOF
	close(stream.recvCh)
	_ = srv.Watch(stream)
	return stream.getSent()
}

// TestWatch_CompactedStartRevision_CreatedThenCompactRevision: a start
// revision at or below the floor gets etcd's compacted watch -- the watch is
// created, then cancelled by a response carrying the floor as CompactRevision.
func TestWatch_CompactedStartRevision_CreatedThenCompactRevision(t *testing.T) {
	for name, refusal := range map[string]error{
		"bare":    &watch.CompactedError{CompactRevision: 7},
		"wrapped": &wrappedWatchError{cause: &watch.CompactedError{CompactRevision: 7}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := &watchServer{watchSvc: refusingWatchServicer(refusal), clusterID: 1, memberID: 2}
			sent := runCreate(t, srv, &pb.WatchCreateRequest{Key: []byte("k"), StartRevision: 3, WatchId: 5})
			if len(sent) != 2 {
				t.Fatalf("sent %d responses, want 2: %+v", len(sent), sent)
			}
			if !sent[0].Created || sent[0].Canceled || sent[0].WatchId != 5 {
				t.Fatalf("first response = %+v, want the watch created", sent[0])
			}
			if sent[1].WatchId != 5 || sent[1].CompactRevision != 7 || !sent[1].Canceled || sent[1].Created {
				t.Fatalf("second response = %+v, want watch 5 cancelled with CompactRevision 7", sent[1])
			}
		})
	}
}

// TestWatch_CompactedStartRevision_SendErrorEndsTheStream: a failed send of
// the compacted watch's created response ends the stream with that error.
func TestWatch_CompactedStartRevision_SendErrorEndsTheStream(t *testing.T) {
	srv := &watchServer{watchSvc: refusingWatchServicer(&watch.CompactedError{CompactRevision: 7}), clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())
	sendErr := errors.New("send failed")
	stream.sendErr = sendErr
	stream.recvCh <- &pb.WatchRequest{RequestUnion: &pb.WatchRequest_CreateRequest{
		CreateRequest: &pb.WatchCreateRequest{Key: []byte("k"), StartRevision: 3},
	}}
	close(stream.recvCh)

	if err := srv.Watch(stream); !errors.Is(err, sendErr) {
		t.Fatalf("Watch = %v, want the send error", err)
	}
}

// TestWatch_CompactedWithoutAFloorIsAFailedCreate: a compaction refusal
// carrying no floor cannot be told to a client as a CompactRevision, so it is
// answered as any other refusal, with its reason.
func TestWatch_CompactedWithoutAFloorIsAFailedCreate(t *testing.T) {
	srv := &watchServer{watchSvc: refusingWatchServicer(&watch.CompactedError{}), clusterID: 1, memberID: 2}
	sent := runCreate(t, srv, &pb.WatchCreateRequest{Key: []byte("k"), StartRevision: 3})
	if len(sent) != 1 || !sent[0].Created || !sent[0].Canceled || sent[0].WatchId != invalidWatchID ||
		sent[0].CompactRevision != 0 || sent[0].CancelReason != watch.ErrCompacted.Error() {
		t.Fatalf("sent %+v, want one failed create carrying ErrCompacted's text", sent)
	}
}

// TestForwardEvents_WatchEndedByCompactionSendsCompactRevision: a watch the
// hub cancels because compaction overtook its replay ends on the stream with
// the floor as CompactRevision.
func TestForwardEvents_WatchEndedByCompactionSendsCompactRevision(t *testing.T) {
	srv := &watchServer{clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())
	mw := newMockWatcher(1, 0)
	mw.end(&watch.CompactedError{CompactRevision: 9})

	srv.forwardEvents(stream, 4, mw)

	sent := stream.getSent()
	if len(sent) != 1 || sent[0].WatchId != 4 || sent[0].CompactRevision != 9 || !sent[0].Canceled {
		t.Fatalf("sent %+v, want watch 4 cancelled with CompactRevision 9", sent)
	}
}

// TestForwardEvents_WatchEndedForAReasonIsCanceledWithIt: any other reason a
// watch ends is a cancellation carrying the reason, never silence.
func TestForwardEvents_WatchEndedForAReasonIsCanceledWithIt(t *testing.T) {
	srv := &watchServer{clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())
	mw := newMockWatcher(1, 0)
	mw.end(watch.ErrWatcherOverflow)

	srv.forwardEvents(stream, 4, mw)

	sent := stream.getSent()
	if len(sent) != 1 || sent[0].WatchId != 4 || !sent[0].Canceled || sent[0].CompactRevision != 0 ||
		sent[0].CancelReason != watch.ErrWatcherOverflow.Error() {
		t.Fatalf("sent %+v, want watch 4 cancelled with the overflow reason", sent)
	}
}

// overlapDetectingStream records whether two Sends ever overlapped.
type overlapDetectingStream struct {
	pb.Watch_WatchServer
	inFlight atomic.Int32
	overlap  atomic.Bool
}

func (s *overlapDetectingStream) Send(*pb.WatchResponse) error {
	if s.inFlight.Add(1) > 1 {
		s.overlap.Store(true)
	}
	time.Sleep(50 * time.Microsecond)
	s.inFlight.Add(-1)
	return nil
}

// TestSerialWatchStream_SendsOneAtATime: the request loop and every watch's
// forwarding goroutine send on one stream, which gRPC forbids doing
// concurrently; the stream wrapper admits one Send at a time.
func TestSerialWatchStream_SendsOneAtATime(t *testing.T) {
	inner := &overlapDetectingStream{}
	stream := &serialWatchStream{Watch_WatchServer: inner}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 20 {
				_ = stream.Send(&pb.WatchResponse{})
			}
		})
	}
	wg.Wait()
	if inner.overlap.Load() {
		t.Fatal("two Sends on one stream overlapped")
	}

	// The control: the same senders on the bare stream do overlap, so the
	// detector can fail.
	bare := &overlapDetectingStream{}
	for range 16 {
		wg.Go(func() {
			for range 20 {
				_ = bare.Send(&pb.WatchResponse{})
			}
		})
	}
	wg.Wait()
	if !bare.overlap.Load() {
		t.Fatal("the detector saw no overlap on an unserialized stream; it cannot fail")
	}
}

// etcdWatcher serves srv's Watch over an in-memory gRPC connection and returns
// the etcd client's own Watcher on it, so the responses are judged by the
// client that consumes them.
func etcdWatcher(t *testing.T, srv *watchServer) clientv3.Watcher {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	pb.RegisterWatchServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	w := clientv3.NewWatchFromWatchClient(pb.NewWatchClient(conn), clientv3.NewCtxClient(context.Background()))
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// nextResponse reads the next response from ch, failing if none arrives.
func nextResponse(t *testing.T, ch clientv3.WatchChan) (clientv3.WatchResponse, bool) {
	t.Helper()
	select {
	case resp, ok := <-ch:
		return resp, ok
	case <-time.After(5 * time.Second):
		t.Fatal("the etcd client received no response")
		return clientv3.WatchResponse{}, false
	}
}

// openWatch opens an etcd client watch, failing if the client's Watch call
// does not return. The client blocks there until the server answers the
// create in a shape it can match, so a create it cannot match hangs it. The
// watch's context has no deadline: a hang must fail here or in nextResponse,
// not quietly close the channel.
func openWatch(t *testing.T, w clientv3.Watcher, key string, opts ...clientv3.OpOption) clientv3.WatchChan {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opened := make(chan clientv3.WatchChan, 1)
	go func() { opened <- w.Watch(ctx, key, opts...) }()
	select {
	case ch := <-opened:
		return ch
	case <-time.After(5 * time.Second):
		t.Fatal("the etcd client's Watch never returned: it could not match the create's answer")
		return nil
	}
}

// TestEtcdClient_CompactedStartRevisionIsErrCompacted: an etcd client
// watching from a compacted revision gets ErrCompacted and the floor, and its
// watch channel closes -- not a hang.
func TestEtcdClient_CompactedStartRevisionIsErrCompacted(t *testing.T) {
	refusal := &wrappedWatchError{cause: &watch.CompactedError{CompactRevision: 7}}
	w := etcdWatcher(t, &watchServer{watchSvc: refusingWatchServicer(refusal), clusterID: 1, memberID: 2})
	ch := openWatch(t, w, "k", clientv3.WithRev(3))
	resp, ok := nextResponse(t, ch)
	if !ok {
		t.Fatal("the watch channel closed with no response")
	}
	if resp.CompactRevision != 7 || !errors.Is(resp.Err(), rpctypes.ErrCompacted) {
		t.Fatalf("response = CompactRevision %d, Err %v; want 7 and ErrCompacted", resp.CompactRevision, resp.Err())
	}
	for ok {
		_, ok = nextResponse(t, ch)
	}
}

// TestEtcdClient_WatchOvertakenByCompactionIsErrCompacted: a watch the hub
// cancels mid-replay reaches the etcd client as ErrCompacted with the floor.
func TestEtcdClient_WatchOvertakenByCompactionIsErrCompacted(t *testing.T) {
	mw := newMockWatcher(1, 0)
	w := etcdWatcher(t, &watchServer{watchSvc: servingWatchServicer(mw), clusterID: 1, memberID: 2})
	ch := openWatch(t, w, "k", clientv3.WithRev(3), clientv3.WithCreatedNotify())
	if created, ok := nextResponse(t, ch); !ok || !created.Created {
		t.Fatalf("first response = %+v, want the created notification", created)
	}
	mw.end(&watch.CompactedError{CompactRevision: 9})

	resp, ok := nextResponse(t, ch)
	if !ok || resp.CompactRevision != 9 || !errors.Is(resp.Err(), rpctypes.ErrCompacted) {
		t.Fatalf("response = %+v (open %v), Err %v; want CompactRevision 9 and ErrCompacted", resp, ok, resp.Err())
	}
}

// TestEtcdClient_RefusedCreateEndsTheWatch: any other refusal ends the etcd
// client's watch with an error instead of leaving it waiting forever.
func TestEtcdClient_RefusedCreateEndsTheWatch(t *testing.T) {
	w := etcdWatcher(t, &watchServer{watchSvc: refusingWatchServicer(errors.New("refused")), clusterID: 1, memberID: 2})
	ch := openWatch(t, w, "k")
	for {
		resp, ok := nextResponse(t, ch)
		if !ok {
			return // closed: the client knows the watch is over
		}
		if resp.Canceled && resp.Err() != nil {
			return
		}
	}
}
