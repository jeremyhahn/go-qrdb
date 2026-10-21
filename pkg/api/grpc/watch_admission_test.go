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
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// streamAdmitterSwitch admits until refuse is called.
type streamAdmitterSwitch struct {
	mu  sync.Mutex
	err error
}

func (a *streamAdmitterSwitch) AdmitStream(context.Context, *auth.Identity) (time.Duration, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return authmw.TenantStatusTTL, a.err
}

func (a *streamAdmitterSwitch) refuse(err error) {
	a.mu.Lock()
	a.err = err
	a.mu.Unlock()
}

// admitPushWatcher is a watcher the test pushes responses onto; its Close
// is safe from the admission forwarder and the handler at once.
type admitPushWatcher struct{ events chan watch.WatchResponse }

func (w *admitPushWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *admitPushWatcher) WatchID() int64                     { return 1 }
func (w *admitPushWatcher) Close() error                       { return nil }
func (w *admitPushWatcher) Err() error                         { return nil }

// A watch opened before its tenant was suspended ends on its next event
// with the status a new request from that tenant gets.
func TestGRPC_Watch_TenantSuspendedEndsStreamOnNextEvent(t *testing.T) {
	src := &admitPushWatcher{events: make(chan watch.WatchResponse, 2)}
	admitter := &streamAdmitterSwitch{}
	watchSvc := service.NewAdmittedWatch(&mockWatchServiceImpl{watcher: src}, admitter)
	client, cleanup := startTestServerFull(t, WithWatch(watchSvc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := client.Watch(ctx, &pb.WatchRequest{Key: []byte("k")})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	src.events <- watch.WatchResponse{WatchID: 1, Revision: 1}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Recv while active: %v", err)
	}

	refusal := &authmw.TenantNotActiveError{TenantID: "acme", Status: "suspended"}
	admitter.refuse(refusal)
	src.events <- watch.WatchResponse{WatchID: 1, Revision: 2}
	_, err = stream.Recv()
	want := status.Code(mapServiceError(refusal))
	if got := status.Code(err); got != codes.PermissionDenied || got != want {
		t.Fatalf("Recv after suspension = %v (code %s), want %s like a new request", err, got, want)
	}
}
