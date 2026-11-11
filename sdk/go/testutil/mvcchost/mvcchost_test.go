// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mvcchost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// fatalRecorder is a testing.TB whose Fatalf records the message and ends the
// calling goroutine, as testing.T's does, so a test can observe a helper
// failing without failing itself.
type fatalRecorder struct {
	testing.TB

	failed bool
	msg    string
}

func (r *fatalRecorder) Helper() {}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.failed = true
	r.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// record runs fn against a fatalRecorder on its own goroutine, so a Fatalf
// inside fn ends that goroutine rather than the test.
func record(t *testing.T, fn func(tb testing.TB)) *fatalRecorder {
	t.Helper()
	r := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(r)
	}()
	<-done
	return r
}

func newTransport(t *testing.T, h *Host) *embedded.Transport {
	t.Helper()
	tr, err := embedded.New(
		h.KV(),
		h.Admin(),
		embedded.WithMVCCService(h.MVCC()),
		embedded.WithTransactionService(h.Transaction()),
		embedded.WithBatchService(h.Batch()),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	return tr
}

// stubClient is a BatchRevisionClient whose answers a test sets, for the
// failure paths a real engine never produces.
type stubClient struct {
	batch    *transport.BatchResponse
	batchErr error
	kv       *transport.MVCCKeyValue
	getErr   error
}

func (s *stubClient) ExecuteBatch(context.Context, []transport.Op, ...transport.WriteOption) (*transport.BatchResponse, error) {
	return s.batch, s.batchErr
}

func (s *stubClient) GetRevision(context.Context, string, ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	return s.kv, s.getErr
}

var _ BatchRevisionClient = (*stubClient)(nil)
var _ BatchRevisionClient = (*embedded.Transport)(nil)

func TestNew_WritesNothingToDisk(t *testing.T) {
	scratch := t.TempDir()
	t.Chdir(scratch)
	h := New(t)
	tr := newTransport(t, h)
	ctx := context.Background()
	if err := tr.Put(ctx, "disk/key", []byte("value")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	kv, err := tr.GetRevision(ctx, "disk/key")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if string(kv.Value) != "value" {
		t.Fatalf("value = %q, want %q", kv.Value, "value")
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatalf("read the test's temp directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("New wrote %d entries under the test's temp directory, want none", len(entries))
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s on the host = %v, want it absent", dir, err)
	}
}

func TestNew_HostsDoNotShareData(t *testing.T) {
	a, b := New(t), New(t)
	ta, tb := newTransport(t, a), newTransport(t, b)
	ctx := context.Background()
	if err := ta.Put(ctx, "only/a", []byte("a")); err != nil {
		t.Fatalf("Put on a: %v", err)
	}
	if kv, err := ta.GetRevision(ctx, "only/a"); err != nil || string(kv.Value) != "a" {
		t.Fatalf("GetRevision on a = %v, %v; want the value a stored", kv, err)
	}
	if kv, err := tb.GetRevision(ctx, "only/a"); err == nil {
		t.Fatalf("GetRevision on b = %v, want an error: b saw a's write", kv)
	}
}

func TestNew_FailsWhenTheFilesystemFails(t *testing.T) {
	fs := errorfs.Wrap(vfs.NewMem(), errorfs.InjectorFunc(func(errorfs.Op) error {
		return errorfs.ErrInjected
	}))
	r := record(t, func(tb testing.TB) { open(tb, fs) })
	if !r.failed || !strings.Contains(r.msg, "open MVCC state machine") {
		t.Fatalf("open on a failing filesystem: failed=%v msg=%q, want an open failure", r.failed, r.msg)
	}
}

func TestHost_SyncProposeAdvancesTheIndex(t *testing.T) {
	h := New(t)
	tr := newTransport(t, h)
	first := CheckBatchRevision(t, tr, "a/")
	second := CheckBatchRevision(t, tr, "b/")
	if second <= first {
		t.Fatalf("second batch revision %d, want > first %d", second, first)
	}
	if h.index < 2 {
		t.Fatalf("applied index after two proposals = %d, want >= 2", h.index)
	}
}

func TestHost_SyncProposeRejectsAMalformedCommand(t *testing.T) {
	h := New(t)
	res, err := h.SyncPropose(context.Background(), ShardID, []byte{0xff, 0xff, 0xff})
	if err == nil && res.Value == 0 && res.Data == nil {
		t.Fatalf("SyncPropose(malformed) = %+v, nil; want an error or an error result", res)
	}
}

func TestHost_SyncReadAnswersFromTheMachine(t *testing.T) {
	h := New(t)
	tr := newTransport(t, h)
	rev := CheckBatchRevision(t, tr, "r/")
	kv, err := tr.GetRevision(context.Background(), "r/put")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if kv.ModRevision != rev || string(kv.Value) != "v" {
		t.Fatalf("read back %+v, want value v at revision %d", kv, rev)
	}
}

func TestHost_SyncReadRejectsAnUnknownQuery(t *testing.T) {
	h := New(t)
	if _, err := h.SyncRead(context.Background(), ShardID, struct{}{}); err == nil {
		t.Fatal("SyncRead(unknown query) = nil error, want an error")
	}
}

func TestHost_ServicesAreNonNil(t *testing.T) {
	h := New(t)
	services := map[string]any{
		"KV":          h.KV(),
		"Admin":       h.Admin(),
		"MVCC":        h.MVCC(),
		"Transaction": h.Transaction(),
		"Batch":       h.Batch(),
	}
	for name, svc := range services {
		if svc == nil {
			t.Errorf("%s() = nil", name)
		}
	}
}

func TestHost_ServicesShareOneMachine(t *testing.T) {
	h := New(t)
	ctx := context.Background()
	resp, err := newTransport(t, h).ExecuteBatch(ctx, []transport.Op{
		{Type: transport.OpPut, Key: []byte("shared"), Value: []byte("v")},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	kv, err := newTransport(t, h).GetRevision(ctx, "shared")
	if err != nil {
		t.Fatalf("GetRevision through a second set of services: %v", err)
	}
	if kv.ModRevision != resp.Revision {
		t.Fatalf("second services read revision %d, want the first's %d", kv.ModRevision, resp.Revision)
	}
}

func TestCheckBatchRevision_PassesOnTheRealEngine(t *testing.T) {
	tr := newTransport(t, New(t))
	r := record(t, func(tb testing.TB) { CheckBatchRevision(tb, tr, "ok/") })
	if r.failed {
		t.Fatalf("CheckBatchRevision on the real engine failed: %s", r.msg)
	}
}

func TestCheckBatchRevision_FailsOnEachDefect(t *testing.T) {
	cases := map[string]struct {
		client *stubClient
		want   string
	}{
		"batch error": {
			client: &stubClient{batchErr: errors.New("boom")},
			want:   "ExecuteBatch: boom",
		},
		"zero revision": {
			client: &stubClient{batch: &transport.BatchResponse{}},
			want:   "want > 0",
		},
		"read error": {
			client: &stubClient{batch: &transport.BatchResponse{Revision: 3}, getErr: errors.New("gone")},
			want:   "GetRevision",
		},
		"revision mismatch": {
			client: &stubClient{
				batch: &transport.BatchResponse{Revision: 3},
				kv:    &transport.MVCCKeyValue{ModRevision: 2},
			},
			want: "want the batch revision 3",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := record(t, func(tb testing.TB) { CheckBatchRevision(tb, tc.client, "p/") })
			if !r.failed || !strings.Contains(r.msg, tc.want) {
				t.Fatalf("failed=%v msg=%q, want a failure containing %q", r.failed, r.msg, tc.want)
			}
		})
	}
}

func TestCheckBatchRevision_ReturnsTheStubbedRevision(t *testing.T) {
	c := &stubClient{
		batch: &transport.BatchResponse{Revision: 7},
		kv:    &transport.MVCCKeyValue{ModRevision: 7},
	}
	if got := CheckBatchRevision(t, c, "s/"); got != 7 {
		t.Fatalf("CheckBatchRevision = %d, want 7", got)
	}
}
