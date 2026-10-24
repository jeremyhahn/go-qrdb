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
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// admitThenRefuse admits its first n checks and refuses every later one,
// standing in for a tenant suspended after n events were delivered.
type admitThenRefuse struct {
	mu      sync.Mutex
	n       int
	refusal error
}

func (a *admitThenRefuse) AdmitStream(context.Context, *auth.Identity) (time.Duration, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.n > 0 {
		a.n--
		return authmw.TenantStatusTTL, nil
	}
	return 0, a.refusal
}

// A watch opened before its tenant was suspended ends on its next event
// with the status a new request from that tenant gets.
func TestHandleWatchStream_TenantSuspendedEndsStreamOnNextEvent(t *testing.T) {
	src := newIdleWatcher()
	refusal := &authmw.TenantNotActiveError{TenantID: "acme", Status: "suspended"}
	ws := service.NewAdmittedWatch(&mockWatchService{
		watchFn: func(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) { return src, nil },
	}, &admitThenRefuse{n: 1, refusal: refusal})
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{}, WithWatch(ws))
	conn := env.dialServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer stream.Close()
	payload, err := proto.Marshal(&pb.WatchRequest{Key: []byte("k")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := wire.WriteRequest(stream, watchMethodKey, "", 1, payload); err != nil {
		t.Fatalf("write watch request: %v", err)
	}

	src.events <- watch.WatchResponse{WatchID: 1, Revision: 1}
	_, st, data, err := wire.ReadResponse(stream)
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	first := &pb.WatchResponse{}
	if err := proto.Unmarshal(data, first); err != nil || st != wire.StatusOK || first.GetCanceled() {
		t.Fatalf("first frame = (%s, %v, %v), want an OK event", wire.StatusName(st), first, err)
	}

	src.events <- watch.WatchResponse{WatchID: 1, Revision: 2}
	_, st, _, err = wire.ReadResponse(stream)
	if err != nil {
		t.Fatalf("read terminal frame: %v", err)
	}
	want, _ := mapServiceError(refusal)
	if st != wire.StatusPermissionDenied || st != want {
		t.Fatalf("terminal status = %s, want %s like a new request", wire.StatusName(st), wire.StatusName(want))
	}
	select {
	case <-src.closed:
	case <-ctx.Done():
		t.Fatal("the source watcher was not closed after the refusal")
	}
}
