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

package watch

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Post-replay Progress watermark (#210)
//
// Created's Revision is captured before a single historical event has been
// delivered, so a caller that treats it as a delivery watermark can resume
// past events a truncated collection never got to (pkg/api/mcp/tools_watch.go
// is exactly such a caller). Progress is the value that IS safe to resume
// from: startReplay only emits it after finishReplay has flushed every
// staged live event, and only when the caller opted in via
// WithProgressNotify — see hub.go's startReplay for the delivery-ordering
// argument.
// ---------------------------------------------------------------------------

func TestWatch_StartRevision_ProgressNotify_WatermarkAfterFullReplay(t *testing.T) {
	h := NewHub()
	h.SetHistory(&fakeHistory{
		current: 3,
		changes: []fakeChange{
			{1, "k", false, "v1"},
			{2, "k", false, "v2"},
			{3, "k", false, "v3"},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := h.Watch(ctx, "k", WithStartRevision(1), WithProgressNotify())
	if err != nil {
		t.Fatal(err)
	}

	// Created, rev1, rev2, rev3, then the post-replay watermark.
	got := collectN(t, w, 5)
	if !got[0].Created || got[0].Revision != 3 {
		t.Fatalf("first response = %+v, want Created with revision 3", got[0])
	}
	for i, wantRev := range []int64{1, 2, 3} {
		r := got[i+1]
		if r.Created || r.Progress || len(r.Events) != 1 || r.Revision != wantRev {
			t.Fatalf("response[%d] = %+v, want a plain event at revision %d", i+1, r, wantRev)
		}
	}
	last := got[4]
	if !last.Progress {
		t.Fatalf("last response = %+v, want Progress=true", last)
	}
	if last.Created {
		t.Fatalf("Progress response must not also claim Created: %+v", last)
	}
	if len(last.Events) != 0 {
		t.Fatalf("Progress response carried events, want none: %+v", last)
	}
	if last.Revision != 3 {
		t.Fatalf("Progress.Revision = %d, want 3 (the replay boundary)", last.Revision)
	}
}

// TestWatch_StartRevision_ProgressNotify_WatermarkOnEmptyReplay covers the
// idle case that motivates the whole mechanism: a replay with nothing to
// deliver must still let the caller's cursor advance, or a poller resuming
// from the same start_revision on every call never converges. Without the
// watermark, a caller that (correctly, post-#210) stops trusting Created
// would see zero events and be unable to distinguish "nothing changed" from
// "truncated mid-replay" — Progress is what tells them apart.
func TestWatch_StartRevision_ProgressNotify_WatermarkOnEmptyReplay(t *testing.T) {
	h := NewHub()
	h.SetHistory(&fakeHistory{current: 5}) // no changes recorded at all
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := h.Watch(ctx, "k", WithStartRevision(1), WithProgressNotify())
	if err != nil {
		t.Fatal(err)
	}

	got := collectN(t, w, 2) // Created, then Progress; no events in between.
	if !got[0].Created || got[0].Revision != 5 {
		t.Fatalf("first response = %+v, want Created with revision 5", got[0])
	}
	if !got[1].Progress || got[1].Revision != 5 || len(got[1].Events) != 0 {
		t.Fatalf("second response = %+v, want empty Progress at revision 5", got[1])
	}
}

// TestWatch_StartRevision_NoProgressNotify_NoWatermark verifies the opt-in
// gate: a replay caller that never asked for WithProgressNotify gets exactly
// the historical events and nothing else, so this is additive — no existing
// consumer of pkg/watch observes a new response type it didn't ask for.
func TestWatch_StartRevision_NoProgressNotify_NoWatermark(t *testing.T) {
	h := NewHub()
	h.SetHistory(&fakeHistory{
		current: 1,
		changes: []fakeChange{{1, "k", false, "v1"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := h.Watch(ctx, "k", WithStartRevision(1))
	if err != nil {
		t.Fatal(err)
	}

	got := collectN(t, w, 2) // Created, rev1 — and nothing more should follow.
	if !got[0].Created {
		t.Fatalf("first response not Created: %+v", got[0])
	}
	if got[1].Progress {
		t.Fatalf("unexpected Progress response with ProgressNotify unset: %+v", got[1])
	}

	select {
	case resp, ok := <-w.Events():
		if ok {
			t.Fatalf("unexpected extra response with ProgressNotify unset: %+v", resp)
		}
	case <-time.After(150 * time.Millisecond):
		// No third response arrived — the expected outcome.
	}
}

// TestWatch_StartRevision_ProgressNotify_NoWatermarkOnCompactionRace verifies
// the fail-closed path (replay raced by compaction) does NOT emit a Progress
// response — finishReplay is only reached on the success path, so a caller
// gating truncation-detection on "no Progress arrived" is not fooled by a
// terminal error masquerading as a clean, if quiet, completion.
func TestWatch_StartRevision_ProgressNotify_NoWatermarkOnCompactionRace(t *testing.T) {
	h := NewHub()
	fh := &fakeHistory{
		current: 3,
		changes: []fakeChange{{1, "k", false, "v1"}},
		gate:    make(chan struct{}),
		firstN:  1,
	}
	h.SetHistory(fh)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := h.Watch(ctx, "k", WithStartRevision(1), WithProgressNotify())
	if err != nil {
		t.Fatal(err)
	}

	// Compact past the start revision while the replay is blocked mid-scan,
	// then release it: startReplay's fail-closed branch fires. The write
	// happens-before the read in startReplay's goroutine via close(fh.gate)
	// (channel close happens-before the receive it unblocks), so this needs
	// no lock of its own.
	fh.compacted = 1
	close(fh.gate)

	deadline := time.After(3 * time.Second)
	var sawProgress bool
	for {
		select {
		case resp, ok := <-w.Events():
			if !ok {
				if sawProgress {
					t.Fatal("Progress response observed on a replay the hub failed closed")
				}
				return
			}
			if resp.Progress {
				sawProgress = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for the watcher to be evicted after the compaction race")
		}
	}
}
