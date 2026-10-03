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

package clusterseal

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

func ended(s *Subscription) bool {
	select {
	case <-s.Done():
		return true
	default:
		return false
	}
}

func TestStreamsEndByBarrier(t *testing.T) {
	cause := errors.New("sealed")
	tests := []struct {
		name string
		end  func(*Streams)
		want map[string]bool // subscription label -> ended
	}{
		{"all", func(s *Streams) { s.EndAll(cause) },
			map[string]bool{"acme/a": true, "acme/b": true, "other/a": true, "system/system": true}},
		{"tenant", func(s *Streams) { s.EndTenant("acme", cause) },
			map[string]bool{"acme/a": true, "acme/b": true, "other/a": false, "system/system": false}},
		{"database", func(s *Streams) { s.EndDatabase("acme", "a", cause) },
			map[string]bool{"acme/a": true, "acme/b": false, "other/a": false, "system/system": false}},
		{"unknown tenant", func(s *Streams) { s.EndTenant("nobody", cause) },
			map[string]bool{"acme/a": false, "acme/b": false, "other/a": false, "system/system": false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStreams()
			subs := map[string]*Subscription{
				"acme/a":        s.Subscribe("acme", "a"),
				"acme/b":        s.Subscribe("acme", "b"),
				"other/a":       s.Subscribe("other", "a"),
				"system/system": s.Subscribe("system", "system"),
			}
			tc.end(s)
			for label, sub := range subs {
				if got := ended(sub); got != tc.want[label] {
					t.Errorf("%s ended = %v, want %v", label, got, tc.want[label])
				}
				if tc.want[label] && !errors.Is(sub.Cause(), cause) {
					t.Errorf("%s cause = %v, want %v", label, sub.Cause(), cause)
				}
				if !tc.want[label] && sub.Cause() != nil {
					t.Errorf("%s cause = %v before it ended", label, sub.Cause())
				}
			}
			ends := 0
			for _, want := range tc.want {
				if want {
					ends++
				}
			}
			if got := s.Len(); got != len(subs)-ends {
				t.Fatalf("Len after end = %d, want %d: an ended subscription must be forgotten", got, len(subs)-ends)
			}
		})
	}
}

func TestStreamsReleaseForgetsWithoutEnding(t *testing.T) {
	s := NewStreams()
	sub := s.Subscribe("acme", "a")
	sub.Release()
	sub.Release() // idempotent
	if s.Len() != 0 {
		t.Fatalf("Len = %d after release", s.Len())
	}
	s.EndAll(errors.New("late"))
	if ended(sub) {
		t.Fatal("a released subscription was ended by a later seal")
	}
}

func TestStreamsEndIsFirstCauseWins(t *testing.T) {
	s := NewStreams()
	sub := s.Subscribe("acme", "a")
	first, second := errors.New("first"), errors.New("second")
	s.EndTenant("acme", first)
	sub.end(second)
	if !errors.Is(sub.Cause(), first) {
		t.Fatalf("cause = %v, want the first", sub.Cause())
	}
}

func TestStreamsConcurrentSubscribeAndEnd(t *testing.T) {
	s := NewStreams()
	var wg sync.WaitGroup
	subs := make(chan *Subscription, 200)
	for range 100 {
		wg.Add(2)
		go func() { defer wg.Done(); subs <- s.Subscribe("acme", "a") }()
		go func() { defer wg.Done(); s.EndTenant("acme", errors.New("sealed")) }()
	}
	wg.Wait()
	s.EndAll(errors.New("final"))
	close(subs)
	for sub := range subs {
		if !ended(sub) {
			t.Fatal("a subscription survived EndAll")
		}
	}
	if s.Len() != 0 {
		t.Fatalf("Len = %d", s.Len())
	}
}

// TestStreams_GuardWatcher_EndsOnItsBarrier: a guarded watch subscribes on
// its (tenant, database), ends with the seal's cause when that barrier is
// sealed, and leaves the registry once ended.
func TestStreams_GuardWatcher_EndsOnItsBarrier(t *testing.T) {
	s := NewStreams()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	w, err := s.GuardWatcher("acme", "ledger", nil, func() (watch.Watcher, error) {
		return hub.Watch(context.Background(), "k")
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1", s.Len())
	}
	s.EndDatabase("acme", "other", errors.New("other"))
	cause := errors.New("ledger sealed")
	s.EndDatabase("acme", "ledger", cause)
	for range w.Events() {
	}
	if err := w.Err(); !errors.Is(err, cause) {
		t.Fatalf("Err = %v, want the seal's cause", err)
	}
	if s.Len() != 0 {
		t.Fatalf("Len after the seal = %d, want 0", s.Len())
	}
}

// TestStreams_GuardWatcher_CloseReleases: closing a guarded watch forgets its
// subscription, so a later seal has nothing of it to end.
func TestStreams_GuardWatcher_CloseReleases(t *testing.T) {
	s := NewStreams()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	w, err := s.GuardWatcher("acme", "ledger", nil, func() (watch.Watcher, error) {
		return hub.Watch(context.Background(), "k")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 {
		t.Fatalf("Len after Close = %d, want 0", s.Len())
	}
	for range w.Events() {
	}
	if err := w.Err(); err != nil {
		t.Fatalf("Err after Close = %v, want nil", err)
	}
}

// TestStreams_SubscribeServing_RefusalReleases: a refusal asked after the
// subscription releases it and is returned; a stream that may open stays
// subscribed.
func TestStreams_SubscribeServing_RefusalReleases(t *testing.T) {
	s := NewStreams()
	refused := errors.New("sealed")
	if _, err := s.SubscribeServing("acme", "ledger", func() error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if s.Len() != 0 {
		t.Fatalf("Len after a refused open = %d, want 0", s.Len())
	}
	sub, err := s.SubscribeServing("acme", "ledger", func() error { return nil })
	if err != nil || sub == nil || s.Len() != 1 {
		t.Fatalf("SubscribeServing = %v, %v; Len %d", sub, err, s.Len())
	}
	sub.Release()
}

// TestStreams_SubscribeServing_SubscribesBeforeItAsks pins the order that
// makes a seal unmissable: the refusal is asked with the subscription already
// registered, so an End* that runs during the check reaches it.
func TestStreams_SubscribeServing_SubscribesBeforeItAsks(t *testing.T) {
	s := NewStreams()
	cause := errors.New("sealed during the check")
	sub, err := s.SubscribeServing("acme", "ledger", func() error {
		s.EndAll(cause)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ended(sub) || !errors.Is(sub.Cause(), cause) {
		t.Fatal("an End* during the refusal check missed the subscription")
	}
}

// TestStreams_GuardWatcher_RefusalOpensNothing: a refused or failed open
// leaves no subscription behind, and the refused open never calls open.
func TestStreams_GuardWatcher_RefusalOpensNothing(t *testing.T) {
	s := NewStreams()
	refused := errors.New("sealed")
	opened := false
	if _, err := s.GuardWatcher("acme", "ledger", func() error { return refused }, func() (watch.Watcher, error) {
		opened = true
		return nil, nil
	}); !errors.Is(err, refused) || opened {
		t.Fatalf("err = %v, opened = %v; want the refusal and no open", err, opened)
	}
	boom := errors.New("open failed")
	if _, err := s.GuardWatcher("acme", "ledger", nil, func() (watch.Watcher, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the open's error", err)
	}
	if s.Len() != 0 {
		t.Fatalf("Len = %d after refused and failed opens, want 0", s.Len())
	}
}

// TestStreams_SubscribeTenantServing: a tenant-wide subscription ends on a
// seal of any of the tenant's databases, of the tenant, or of the node, and
// not on another tenant's; a refusal asked after it releases it.
func TestStreams_SubscribeTenantServing(t *testing.T) {
	ends := map[string]func(*Streams){
		"one of its databases": func(s *Streams) { s.EndDatabase("acme", "ledger", errors.New("db")) },
		"the tenant":           func(s *Streams) { s.EndTenant("acme", errors.New("tenant")) },
		"the node":             func(s *Streams) { s.EndAll(errors.New("node")) },
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			s := NewStreams()
			sub, err := s.SubscribeTenantServing("acme", nil)
			if err != nil {
				t.Fatal(err)
			}
			s.EndDatabase("globex", "ledger", errors.New("other tenant"))
			s.EndTenant("globex", errors.New("other tenant"))
			if ended(sub) {
				t.Fatal("another tenant's seal ended the session")
			}
			end(s)
			if !ended(sub) {
				t.Fatalf("a seal of %s did not end the session", name)
			}
		})
	}
	s := NewStreams()
	refused := errors.New("sealed")
	if _, err := s.SubscribeTenantServing("acme", func() error { return refused }); !errors.Is(err, refused) || s.Len() != 0 {
		t.Fatalf("err = %v, Len = %d; want the refusal and nothing subscribed", err, s.Len())
	}
}
