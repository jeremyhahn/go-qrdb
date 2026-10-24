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

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

// admitPushWatcher is a watcher preloaded with responses; its Close is
// safe from the admission forwarder and the handler at once.
type admitPushWatcher struct{ events chan watch.WatchResponse }

func (w *admitPushWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *admitPushWatcher) WatchID() int64                     { return 1 }
func (w *admitPushWatcher) Close() error                       { return nil }
func (w *admitPushWatcher) Err() error                         { return nil }

// A watch opened before its tenant was suspended ends on its next event:
// the event is not delivered and the stream closes with the terminal
// canceled frame carrying the refusal, the in-band end every SSE watch
// takes once its 200 is on the wire.
func TestHandleWatch_TenantSuspendedEndsStreamOnNextEvent(t *testing.T) {
	src := &admitPushWatcher{events: make(chan watch.WatchResponse, 2)}
	src.events <- watch.WatchResponse{WatchID: 1, Revision: 1}
	src.events <- watch.WatchResponse{WatchID: 1, Revision: 2}
	refusal := &authmw.TenantNotActiveError{TenantID: "acme", Status: "suspended"}
	watchSvc := service.NewAdmittedWatch(&mockWatchService{
		watchFn: func(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) { return src, nil },
	}, &admitThenRefuse{n: 1, refusal: refusal})
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	rec := newFlushableRecorder()
	srv.handleWatch(rec, httptest.NewRequest(http.MethodGet, "/watch?key=mykey", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var frames []watchEventJSON
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var f watchEventJSON
		if err := json.Unmarshal([]byte(data), &f); err != nil {
			t.Fatalf("frame %q: %v", data, err)
		}
		frames = append(frames, f)
	}
	if len(frames) != 2 || frames[0].Revision != 1 || frames[0].Canceled {
		t.Fatalf("frames = %+v, want revision 1 then the terminal frame", frames)
	}
	if !frames[1].Canceled || frames[1].CancelReason != refusal.Error() || frames[1].Revision == 2 {
		t.Fatalf("terminal frame = %+v, want canceled with %q", frames[1], refusal.Error())
	}
}
