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
	"errors"
	"net"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// fakeWatchServicer
// ---------------------------------------------------------------------------

// fakeWatchServicer implements service.WatchServicer, capturing the
// resolved watch.WatchOptions of the last Watch/WatchPrefix call and
// returning a caller-supplied watcher (or error).
type fakeWatchServicer struct {
	watcher    watch.Watcher
	err        error
	lastOpts   watch.WatchOptions
	lastPrefix bool
}

func (f *fakeWatchServicer) Watch(_ context.Context, _ string, opts ...watch.WatchOption) (watch.Watcher, error) {
	f.lastOpts = watch.ApplyWatchOptions(opts)
	f.lastPrefix = false
	if f.err != nil {
		return nil, f.err
	}
	return f.watcher, nil
}

func (f *fakeWatchServicer) WatchPrefix(_ context.Context, _ string, opts ...watch.WatchOption) (watch.Watcher, error) {
	f.lastOpts = watch.ApplyWatchOptions(opts)
	f.lastPrefix = true
	if f.err != nil {
		return nil, f.err
	}
	return f.watcher, nil
}

func (f *fakeWatchServicer) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

func (f *fakeWatchServicer) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

// fakeWatcher implements watch.Watcher over a caller-supplied channel plus a
// terminal error observable after the channel closes (mirroring pkg/watch's
// contract: Events() closes, then Err() reports why).
type fakeWatcher struct {
	ch  chan watch.WatchResponse
	id  int64
	err error
}

func (w *fakeWatcher) Events() <-chan watch.WatchResponse { return w.ch }
func (w *fakeWatcher) WatchID() int64                     { return w.id }
func (w *fakeWatcher) Close() error                       { return nil }
func (w *fakeWatcher) Err() error                         { return w.err }

// ---------------------------------------------------------------------------
// Server / transport harness
// ---------------------------------------------------------------------------

func startWatchTestServer(t *testing.T, watchSvc service.WatchServicer) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterWatch(watchSvc))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func connectWatchTransport(t *testing.T, addr string) (*Transport, func()) {
	t.Helper()
	conn, err := googlegrpc.NewClient(addr, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	tr := &Transport{
		config: &transport.Config{Address: addr},
		conn:   conn,
		client: pb.NewQRDBServiceClient(conn),
	}
	return tr, func() { _ = conn.Close() }
}

// ---------------------------------------------------------------------------
// start-revision threading
// ---------------------------------------------------------------------------

// TestTransport_Watch_StartRevisionThreaded verifies WatchOptions.StartRevision
// reaches the service layer as watch.WithStartRevision over the wire.
func TestTransport_Watch_StartRevisionThreaded(t *testing.T) {
	ch := make(chan watch.WatchResponse)
	close(ch)
	svc := &fakeWatchServicer{watcher: &fakeWatcher{ch: ch, id: 1}}

	addr, stop := startWatchTestServer(t, svc)
	defer stop()
	tr, cleanup := connectWatchTransport(t, addr)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := tr.Watch(ctx, "mykey", transport.WatchOptions{StartRevision: 42})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	for range out {
	}

	if svc.lastOpts.StartRevision != 42 {
		t.Fatalf("StartRevision = %d, want 42", svc.lastOpts.StartRevision)
	}
	if svc.lastPrefix {
		t.Fatal("expected the exact-key path, not prefix")
	}
}

// TestTransport_WatchPrefix_StartRevisionThreaded mirrors the above for
// WatchPrefix.
func TestTransport_WatchPrefix_StartRevisionThreaded(t *testing.T) {
	ch := make(chan watch.WatchResponse)
	close(ch)
	svc := &fakeWatchServicer{watcher: &fakeWatcher{ch: ch, id: 1}}

	addr, stop := startWatchTestServer(t, svc)
	defer stop()
	tr, cleanup := connectWatchTransport(t, addr)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := tr.WatchPrefix(ctx, "prefix/", transport.WatchOptions{StartRevision: 7})
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}
	for range out {
	}

	if svc.lastOpts.StartRevision != 7 {
		t.Fatalf("StartRevision = %d, want 7", svc.lastOpts.StartRevision)
	}
	if !svc.lastPrefix {
		t.Fatal("expected the prefix path")
	}
}

// ---------------------------------------------------------------------------
// Mid-stream cancellation: watch.ErrCompacted surfaced as a status error by
// the server (see errors.go's mapServiceError) must be captured by
// readGRPCWatchResponses and synthesized into a terminal Canceled response,
// not silently dropped.
// ---------------------------------------------------------------------------

func TestTransport_Watch_MidStreamCompacted(t *testing.T) {
	ch := make(chan watch.WatchResponse)
	close(ch) // Events() already closed, as the hub leaves it post-eviction.
	svc := &fakeWatchServicer{watcher: &fakeWatcher{ch: ch, id: 5, err: watch.ErrCompacted}}

	addr, stop := startWatchTestServer(t, svc)
	defer stop()
	tr, cleanup := connectWatchTransport(t, addr)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := tr.Watch(ctx, "mykey", transport.WatchOptions{StartRevision: 1})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	resp, ok := <-out
	if !ok {
		t.Fatal("channel closed without a terminal response")
	}
	if !resp.Canceled {
		t.Fatalf("resp.Canceled = false, want true: %+v", resp)
	}

	var wce *transport.WatchCompactedError
	if !errors.As(resp.Err(), &wce) {
		t.Fatalf("resp.Err() = %T (%v), want *transport.WatchCompactedError", resp.Err(), resp.Err())
	}
}
