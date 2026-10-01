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
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// idleWatcher is a watch.Watcher that never delivers an event: a watch whose
// key nobody writes, which is what a real watch is for almost all of its life.
type idleWatcher struct {
	events    chan watch.WatchResponse
	closeOnce sync.Once
	closed    chan struct{}
}

func newIdleWatcher() *idleWatcher {
	return &idleWatcher{events: make(chan watch.WatchResponse), closed: make(chan struct{})}
}

func (w *idleWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *idleWatcher) WatchID() int64                     { return 1 }
func (w *idleWatcher) Err() error                         { return nil }

func (w *idleWatcher) Close() error {
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}

var _ watch.Watcher = (*idleWatcher)(nil)

// TestServer_Stop_EndsOpenWatchStreams pins that an open watch stream does not
// hold Stop for quicStopGracePeriod. A watch never finishes on its own -- it
// waits for events until its client leaves -- so counting it among in-flight
// requests without ending it made every Stop with a watcher attached (every
// system-scope migration recycle on a node someone is watching) wait out the
// full grace period before the connection-close pass ended it anyway. The
// stream ends with a terminal Canceled frame naming the shutdown, not bare.
func TestServer_Stop_EndsOpenWatchStreams(t *testing.T) {
	watcher := newIdleWatcher()
	started := make(chan struct{})
	ws := &mockWatchService{
		watchFn: func(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
			close(started)
			return watcher, nil
		},
	}
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{}, WithWatch(ws))
	conn := env.dialServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	payload, err := proto.Marshal(&pb.WatchRequest{Key: []byte("k")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := wire.WriteRequest(stream, watchMethodKey, "", 1, payload); err != nil {
		t.Fatalf("write watch request: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never started")
	}

	// The client reads until the server ends the watch, then closes its side
	// -- the acknowledgement the server waits for after its terminal frame.
	type frame struct {
		status uint16
		resp   *pb.WatchResponse
		err    error
	}
	frames := make(chan frame, 1)
	go func() {
		f := frame{resp: &pb.WatchResponse{}}
		var payload []byte
		_, f.status, payload, f.err = wire.ReadResponse(stream)
		if f.err == nil {
			f.err = proto.Unmarshal(payload, f.resp)
		}
		_ = stream.Close()
		frames <- f
	}()

	begun := time.Now()
	limit := quicStopGracePeriod / 2
	select {
	case <-stopAsync(env.srv):
		if waited := time.Since(begun); waited >= limit {
			t.Fatalf("Stop took %v with a watch open, want under %v", waited, limit)
		}
	case <-time.After(quicStopGracePeriod + time.Second):
		t.Fatal("Stop waited out the grace period behind an open watch")
	}

	select {
	case f := <-frames:
		if f.err != nil {
			t.Fatalf("reading the terminal watch frame: %v", f.err)
		}
		if f.status != wire.StatusOK || !f.resp.GetCanceled() || f.resp.GetCancelReason() != watchShutdownReason {
			t.Fatalf("terminal frame = (%s, canceled=%v, reason=%q), want (OK, true, %q)",
				wire.StatusName(f.status), f.resp.GetCanceled(), f.resp.GetCancelReason(), watchShutdownReason)
		}
	case <-time.After(time.Second):
		t.Fatal("no terminal frame: the watch stream ended bare")
	}

	select {
	case <-watcher.closed:
	case <-time.After(time.Second):
		t.Fatal("the watcher was not closed when Stop ended its stream")
	}
}
