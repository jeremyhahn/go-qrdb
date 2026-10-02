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
	"sync"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"google.golang.org/grpc/metadata"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Mock WatchServicer
// ---------------------------------------------------------------------------

type mockWatchServicer struct {
	watchFn       func(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)
	watchPrefixFn func(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)
}

func (m *mockWatchServicer) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	return m.watchFn(ctx, key, opts...)
}

func (m *mockWatchServicer) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	return m.watchPrefixFn(ctx, prefix, opts...)
}

func (m *mockWatchServicer) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	panic("not called")
}

func (m *mockWatchServicer) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	panic("not called")
}

// ---------------------------------------------------------------------------
// Mock Watcher
// ---------------------------------------------------------------------------

type mockWatcher struct {
	events  chan watch.WatchResponse
	watchID int64
	closed  bool
	mu      sync.Mutex
	// err is what Err reports: the reason the watch ended, nil for a clean
	// close.
	err error
}

func newMockWatcher(id int64, buf int) *mockWatcher {
	return &mockWatcher{
		events:  make(chan watch.WatchResponse, buf),
		watchID: id,
	}
}

func (w *mockWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *mockWatcher) WatchID() int64                     { return w.watchID }
func (w *mockWatcher) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		close(w.events)
	}
	return nil
}
func (w *mockWatcher) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// end closes the watcher's events as the hub does when a watch ends for a
// reason: the reason first, then the channel.
func (w *mockWatcher) end(err error) {
	w.mu.Lock()
	w.err = err
	w.mu.Unlock()
	_ = w.Close()
}

// ---------------------------------------------------------------------------
// Mock Watch_WatchServer stream
// ---------------------------------------------------------------------------

type mockWatchStream struct {
	ctx     context.Context
	recvCh  chan *pb.WatchRequest
	recvErr error
	sent    []*pb.WatchResponse
	sendErr error
	mu      sync.Mutex
}

func newMockWatchStream(ctx context.Context) *mockWatchStream {
	return &mockWatchStream{
		ctx:    ctx,
		recvCh: make(chan *pb.WatchRequest, 10),
	}
}

func (s *mockWatchStream) Send(resp *pb.WatchResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, resp)
	return nil
}

func (s *mockWatchStream) Recv() (*pb.WatchRequest, error) {
	req, ok := <-s.recvCh
	if !ok {
		if s.recvErr != nil {
			return nil, s.recvErr
		}
		return nil, io.EOF
	}
	return req, nil
}

func (s *mockWatchStream) getSent() []*pb.WatchResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*pb.WatchResponse, len(s.sent))
	copy(out, s.sent)
	return out
}

func (s *mockWatchStream) Context() context.Context       { return s.ctx }
func (s *mockWatchStream) SetHeader(_ metadata.MD) error  { return nil }
func (s *mockWatchStream) SendHeader(_ metadata.MD) error { return nil }
func (s *mockWatchStream) SetTrailer(_ metadata.MD)       {}
func (s *mockWatchStream) SendMsg(_ interface{}) error    { return nil }
func (s *mockWatchStream) RecvMsg(_ interface{}) error    { return nil }

// ---------------------------------------------------------------------------
// Tests: Watch — CreateRequest
// ---------------------------------------------------------------------------

func TestWatch_CreateSingleKey(t *testing.T) {
	mw := newMockWatcher(1, 1)
	svc := &mockWatchServicer{
		watchFn: func(_ context.Context, key string, _ ...watch.WatchOption) (watch.Watcher, error) {
			if key != "foo" {
				t.Errorf("expected key foo, got %s", key)
			}
			return mw, nil
		},
	}

	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	// Send create request then close stream.
	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{Key: []byte("foo")},
		},
	}
	stream.recvErr = io.EOF
	close(stream.recvCh)

	err := srv.Watch(stream)
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}

	sent := stream.getSent()
	if len(sent) < 1 {
		t.Fatal("expected at least 1 sent response")
	}
	if !sent[0].Created {
		t.Error("expected Created=true")
	}
	if sent[0].WatchId == 0 {
		t.Error("expected non-zero auto-assigned watch ID")
	}
}

func TestWatch_CreateWithExplicitWatchID(t *testing.T) {
	mw := newMockWatcher(42, 0)
	svc := &mockWatchServicer{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return mw, nil
		},
	}

	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{Key: []byte("bar"), WatchId: 42},
		},
	}
	stream.recvErr = io.EOF
	close(stream.recvCh)

	_ = srv.Watch(stream)

	sent := stream.getSent()
	if len(sent) < 1 {
		t.Fatal("expected at least 1 sent response")
	}
	if sent[0].WatchId != 42 {
		t.Errorf("expected watchID 42, got %d", sent[0].WatchId)
	}
}

func TestWatch_CreatePrefixWatch(t *testing.T) {
	var calledPrefix string
	mw := newMockWatcher(1, 0)
	svc := &mockWatchServicer{
		watchPrefixFn: func(_ context.Context, prefix string, _ ...watch.WatchOption) (watch.Watcher, error) {
			calledPrefix = prefix
			return mw, nil
		},
	}

	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{
				Key:      []byte("/prefix/"),
				RangeEnd: []byte("/prefix0"),
			},
		},
	}
	stream.recvErr = io.EOF
	close(stream.recvCh)

	_ = srv.Watch(stream)

	if calledPrefix != "/prefix/" {
		t.Errorf("expected prefix /prefix/, got %s", calledPrefix)
	}
}

func TestWatch_CreateWithStartRevision(t *testing.T) {
	var gotOpts []watch.WatchOption
	mw := newMockWatcher(1, 0)
	svc := &mockWatchServicer{
		watchFn: func(_ context.Context, _ string, opts ...watch.WatchOption) (watch.Watcher, error) {
			gotOpts = opts
			return mw, nil
		},
	}

	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{
				Key:           []byte("key"),
				StartRevision: 100,
			},
		},
	}
	stream.recvErr = io.EOF
	close(stream.recvCh)

	_ = srv.Watch(stream)

	if len(gotOpts) != 1 {
		t.Fatalf("expected 1 watch option, got %d", len(gotOpts))
	}
}

func TestWatch_CreateEmptyKey(t *testing.T) {
	svc := &mockWatchServicer{}
	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{Key: nil},
		},
	}
	stream.recvErr = io.EOF
	close(stream.recvCh)

	_ = srv.Watch(stream)

	sent := stream.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 response, got %d", len(sent))
	}
	if !sent[0].Created || !sent[0].Canceled || sent[0].WatchId != invalidWatchID {
		t.Errorf("empty-key create = %+v, want etcd's failed create", sent[0])
	}
	if sent[0].CancelReason != "key is required" {
		t.Errorf("unexpected cancel reason: %s", sent[0].CancelReason)
	}
}

func TestWatch_CreateWatchError(t *testing.T) {
	watchErr := errors.New("watch failed")
	svc := &mockWatchServicer{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, watchErr
		},
	}

	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{Key: []byte("key")},
		},
	}
	stream.recvErr = io.EOF
	close(stream.recvCh)

	_ = srv.Watch(stream)

	sent := stream.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 response, got %d", len(sent))
	}
	// etcd's failed create: Created and Canceled with no watch ID, so an etcd
	// client matches it to its pending create instead of waiting forever.
	if !sent[0].Created || !sent[0].Canceled || sent[0].WatchId != invalidWatchID {
		t.Errorf("failed create = %+v, want Created, Canceled, WatchId %d", sent[0], invalidWatchID)
	}
	if sent[0].CancelReason != "watch failed" {
		t.Errorf("unexpected cancel reason: %s", sent[0].CancelReason)
	}
}

func TestWatch_CreateEmptyKeySendError(t *testing.T) {
	svc := &mockWatchServicer{}
	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	sendErr := errors.New("send failed")
	stream.sendErr = sendErr

	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{Key: nil},
		},
	}
	close(stream.recvCh)

	err := srv.Watch(stream)
	if err != sendErr {
		t.Fatalf("expected send error, got %v", err)
	}
}

func TestWatch_CreateWatchErrorSendError(t *testing.T) {
	svc := &mockWatchServicer{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, errors.New("watch failed")
		},
	}
	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	sendErr := errors.New("send failed")
	stream.sendErr = sendErr

	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{Key: []byte("key")},
		},
	}
	close(stream.recvCh)

	err := srv.Watch(stream)
	if err != sendErr {
		t.Fatalf("expected send error, got %v", err)
	}
}

func TestWatch_CreateSendCreatedError(t *testing.T) {
	mw := newMockWatcher(1, 0)
	svc := &mockWatchServicer{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return mw, nil
		},
	}
	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	sendErr := errors.New("send created failed")
	stream.sendErr = sendErr

	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{Key: []byte("key")},
		},
	}
	close(stream.recvCh)

	err := srv.Watch(stream)
	if err != sendErr {
		t.Fatalf("expected send error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Watch — CancelRequest
// ---------------------------------------------------------------------------

func TestWatch_Cancel(t *testing.T) {
	mw := newMockWatcher(1, 0)
	svc := &mockWatchServicer{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return mw, nil
		},
	}

	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	// Create then cancel.
	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{Key: []byte("key"), WatchId: 5},
		},
	}
	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CancelRequest{
			CancelRequest: &pb.WatchCancelRequest{WatchId: 5},
		},
	}
	stream.recvErr = io.EOF
	close(stream.recvCh)

	err := srv.Watch(stream)
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}

	sent := stream.getSent()
	if len(sent) < 2 {
		t.Fatalf("expected at least 2 responses, got %d", len(sent))
	}
	// Second response should be cancel.
	if !sent[1].Canceled {
		t.Error("expected Canceled=true")
	}
	if sent[1].WatchId != 5 {
		t.Errorf("expected watchID 5, got %d", sent[1].WatchId)
	}
}

func TestWatch_CancelNonexistent(t *testing.T) {
	svc := &mockWatchServicer{}
	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CancelRequest{
			CancelRequest: &pb.WatchCancelRequest{WatchId: 99},
		},
	}
	stream.recvErr = io.EOF
	close(stream.recvCh)

	_ = srv.Watch(stream)

	sent := stream.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 response, got %d", len(sent))
	}
	if !sent[0].Canceled {
		t.Error("expected Canceled=true")
	}
}

func TestWatch_CancelSendError(t *testing.T) {
	svc := &mockWatchServicer{}
	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	sendErr := errors.New("cancel send failed")
	stream.sendErr = sendErr

	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CancelRequest{
			CancelRequest: &pb.WatchCancelRequest{WatchId: 1},
		},
	}
	close(stream.recvCh)

	err := srv.Watch(stream)
	if err != sendErr {
		t.Fatalf("expected send error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Watch — RecvError
// ---------------------------------------------------------------------------

func TestWatch_RecvError(t *testing.T) {
	svc := &mockWatchServicer{}
	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	recvErr := errors.New("recv failed")
	stream.recvErr = recvErr
	close(stream.recvCh)

	err := srv.Watch(stream)
	if err != recvErr {
		t.Fatalf("expected recv error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: forwardEvents
// ---------------------------------------------------------------------------

func TestForwardEvents_PutEvent(t *testing.T) {
	srv := &watchServer{clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	mw := newMockWatcher(1, 1)
	mw.events <- watch.WatchResponse{
		Revision: 10,
		Events: []*watch.Event{
			{
				Type: watch.EventPut,
				Kv: &kv.KeyValue{
					Key:            []byte("k1"),
					Value:          []byte("v1"),
					CreateRevision: 5,
					ModRevision:    10,
					Version:        2,
				},
			},
		},
	}
	if err := mw.Close(); err != nil {
		t.Errorf("mw.Close: %v", err)
	}

	srv.forwardEvents(stream, 7, mw)

	sent := stream.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 response, got %d", len(sent))
	}
	if sent[0].WatchId != 7 {
		t.Errorf("expected watchID 7, got %d", sent[0].WatchId)
	}
	if sent[0].Header.Revision != 10 {
		t.Errorf("expected revision 10, got %d", sent[0].Header.Revision)
	}
	if len(sent[0].Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sent[0].Events))
	}
	if sent[0].Events[0].Type != mvccpb.PUT {
		t.Error("expected PUT event type")
	}
	if string(sent[0].Events[0].Kv.Key) != "k1" {
		t.Errorf("expected key k1, got %s", sent[0].Events[0].Kv.Key)
	}
}

func TestForwardEvents_DeleteEvent(t *testing.T) {
	srv := &watchServer{clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	mw := newMockWatcher(1, 1)
	mw.events <- watch.WatchResponse{
		Revision: 15,
		Events: []*watch.Event{
			{
				Type: watch.EventDelete,
				Kv: &kv.KeyValue{
					Key:         []byte("k2"),
					ModRevision: 15,
				},
			},
		},
	}
	if err := mw.Close(); err != nil {
		t.Errorf("mw.Close: %v", err)
	}

	srv.forwardEvents(stream, 3, mw)

	sent := stream.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 response, got %d", len(sent))
	}
	if sent[0].Events[0].Type != mvccpb.DELETE {
		t.Error("expected DELETE event type")
	}
}

func TestForwardEvents_WithPrevKv(t *testing.T) {
	srv := &watchServer{clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	mw := newMockWatcher(1, 1)
	mw.events <- watch.WatchResponse{
		Revision: 20,
		Events: []*watch.Event{
			{
				Type: watch.EventPut,
				Kv: &kv.KeyValue{
					Key:         []byte("k3"),
					Value:       []byte("new"),
					ModRevision: 20,
				},
				PrevKv: &kv.KeyValue{
					Key:         []byte("k3"),
					Value:       []byte("old"),
					ModRevision: 19,
				},
			},
		},
	}
	if err := mw.Close(); err != nil {
		t.Errorf("mw.Close: %v", err)
	}

	srv.forwardEvents(stream, 1, mw)

	sent := stream.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 response, got %d", len(sent))
	}
	if sent[0].Events[0].PrevKv == nil {
		t.Fatal("expected PrevKv to be set")
	}
	if string(sent[0].Events[0].PrevKv.Value) != "old" {
		t.Errorf("expected prev value 'old', got '%s'", sent[0].Events[0].PrevKv.Value)
	}
}

func TestForwardEvents_NilEvent(t *testing.T) {
	srv := &watchServer{clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	mw := newMockWatcher(1, 1)
	mw.events <- watch.WatchResponse{
		Revision: 25,
		Events:   []*watch.Event{nil},
	}
	_ = mw.Close()

	srv.forwardEvents(stream, 1, mw)

	sent := stream.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 response, got %d", len(sent))
	}
	if len(sent[0].Events) != 0 {
		t.Errorf("expected 0 events after filtering nil, got %d", len(sent[0].Events))
	}
}

func TestForwardEvents_SendError(t *testing.T) {
	srv := &watchServer{clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())
	stream.sendErr = errors.New("send failed")

	mw := newMockWatcher(1, 2)
	mw.events <- watch.WatchResponse{
		Revision: 30,
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte("k")}},
		},
	}

	// forwardEvents should exit on send error without blocking.
	done := make(chan struct{})
	go func() {
		srv.forwardEvents(stream, 1, mw)
		close(done)
	}()

	select {
	case <-done:
		// Expected: forwardEvents returned due to send error.
	case <-time.After(2 * time.Second):
		t.Fatal("forwardEvents did not exit on send error")
	}
}

func TestForwardEvents_EmptyChannel(t *testing.T) {
	srv := &watchServer{clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	mw := newMockWatcher(1, 0)
	_ = mw.Close() // Close immediately — channel is drained.

	done := make(chan struct{})
	go func() {
		srv.forwardEvents(stream, 1, mw)
		close(done)
	}()

	select {
	case <-done:
		// Expected: forwardEvents returned when channel closed.
	case <-time.After(2 * time.Second):
		t.Fatal("forwardEvents did not exit on closed channel")
	}

	sent := stream.getSent()
	if len(sent) != 0 {
		t.Errorf("expected 0 responses, got %d", len(sent))
	}
}

// ---------------------------------------------------------------------------
// Tests: Watch — defer cleanup
// ---------------------------------------------------------------------------

func TestWatch_DeferedCleanup(t *testing.T) {
	mw := newMockWatcher(1, 0)
	svc := &mockWatchServicer{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return mw, nil
		},
	}

	srv := &watchServer{watchSvc: svc, clusterID: 1, memberID: 2}
	stream := newMockWatchStream(context.Background())

	stream.recvCh <- &pb.WatchRequest{
		RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{Key: []byte("key"), WatchId: 10},
		},
	}
	stream.recvErr = io.EOF
	close(stream.recvCh)

	_ = srv.Watch(stream)

	// After Watch returns, the deferred cleanup should have closed the watcher.
	mw.mu.Lock()
	closed := mw.closed
	mw.mu.Unlock()
	if !closed {
		t.Error("expected watcher to be closed by deferred cleanup")
	}
}
