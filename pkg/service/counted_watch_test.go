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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ctxRecordingWatch records the context each WatchServicer method receives.
type ctxRecordingWatch struct {
	WatchServicer
	ctxs []context.Context
}

func (r *ctxRecordingWatch) Watch(ctx context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	r.ctxs = append(r.ctxs, ctx)
	return nil, nil
}

func (r *ctxRecordingWatch) WatchPrefix(ctx context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	r.ctxs = append(r.ctxs, ctx)
	return nil, nil
}

func (r *ctxRecordingWatch) WatchEntity(ctx context.Context, _ *WatchEntityRequest) (<-chan WatchEntityEvent, error) {
	r.ctxs = append(r.ctxs, ctx)
	return nil, nil
}

func (r *ctxRecordingWatch) WatchEntityType(ctx context.Context, _ *WatchEntityTypeRequest) (<-chan WatchEntityEvent, error) {
	r.ctxs = append(r.ctxs, ctx)
	return nil, nil
}

var _ WatchServicer = (*ctxRecordingWatch)(nil)

// takesASlot reports whether a watch opened with ctx counts against a
// one-slot hub: the second such watch is refused only when ctx is counted.
func takesASlot(t *testing.T, ctx context.Context) bool {
	t.Helper()
	h := watch.NewHub(watch.WithWatcherLimit(watch.NewWatcherLimit(1)))
	defer h.Close()
	for i := 0; i < 2; i++ {
		w, err := h.Watch(ctx, "k")
		var le *watch.WatcherLimitError
		if errors.As(err, &le) {
			return true
		}
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		defer func() { _ = w.Close() }()
	}
	return false
}

func TestNewCountedWatch_NilStaysNil(t *testing.T) {
	if got := NewCountedWatch(nil); got != nil {
		t.Fatalf("NewCountedWatch(nil) = %v, want nil", got)
	}
}

func TestNewCountedWatch_EveryMethodCountsTheWatch(t *testing.T) {
	rec := &ctxRecordingWatch{}
	svc := NewCountedWatch(rec)
	ctx := context.Background()
	_, _ = svc.Watch(ctx, "k")
	_, _ = svc.WatchPrefix(ctx, "p/")
	_, _ = svc.WatchEntity(ctx, &WatchEntityRequest{})
	_, _ = svc.WatchEntityType(ctx, &WatchEntityTypeRequest{})
	if len(rec.ctxs) != 4 {
		t.Fatalf("delegated %d calls, want 4", len(rec.ctxs))
	}
	for i, got := range rec.ctxs {
		if !takesASlot(t, got) {
			t.Errorf("call %d: context is not counted", i)
		}
	}
}

func TestNewCountedWatch_UnwrappedWatchIsNotCounted(t *testing.T) {
	rec := &ctxRecordingWatch{}
	_, _ = rec.WatchPrefix(context.Background(), "p/")
	if takesASlot(t, rec.ctxs[0]) {
		t.Fatal("a watch that bypasses NewCountedWatch must not take a slot")
	}
}
