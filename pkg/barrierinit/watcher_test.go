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

package barrierinit

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// watchFixture is one tenant barrier's owner and a watcher over a memKV.
type watchFixture struct {
	kv    *memKV
	book  *Book
	admin *ecdsa.PrivateKey
	ca    *testCA
	clock *clocktest.Fake

	mu        sync.Mutex
	delivered []InitStatement
	handleErr error
	// servesErr fails the placement read Serves stands for.
	servesErr error
	// placedLater places globex's barrier group on this node, as a
	// scale-out or a rebalance move does after globex's init.
	placedLater bool
	// notify receives each delivered epoch.
	notify chan uint64
}

func newWatchFixture(t *testing.T) *watchFixture {
	t.Helper()
	f := &watchFixture{kv: newMemKV(), admin: newKey(t), ca: newCA(t), clock: clocktest.NewAt(testNow), notify: make(chan uint64, 16)}
	f.book = NewBook(f.kv, nil)
	return f
}

func (f *watchFixture) anchors(t *testing.T) Anchors {
	return Anchors{FirstAdmin: thumbprint(t, f.admin.Public()), CA: f.ca.pool}
}

func (f *watchFixture) config(t *testing.T) WatcherConfig {
	return WatcherConfig{
		KV:      f.kv,
		Anchors: func(context.Context, clusterseal.Barrier) (Anchors, error) { return f.anchors(t), nil },
		Serves: func(_ context.Context, b clusterseal.Barrier) (bool, error) {
			if f.servesErr != nil {
				return false, f.servesErr
			}
			return b == clusterseal.TenantBarrier("acme") || (f.placedLater && b == clusterseal.TenantBarrier("globex")), nil
		},
		Handle: func(_ context.Context, s InitStatement) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.handleErr != nil {
				return f.handleErr
			}
			f.delivered = append(f.delivered, s)
			f.notify <- s.Epoch
			return nil
		},
		Clock: f.clock,
	}
}

func (f *watchFixture) watcher(t *testing.T) *Watcher {
	t.Helper()
	w, err := NewWatcher(f.config(t))
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	return w
}

func (f *watchFixture) claim(t *testing.T, barrier clusterseal.Barrier) {
	t.Helper()
	s := unsigned(1, "")
	s.Barrier = barrier
	if err := f.book.Claim(context.Background(), signed(t, f.admin, s), f.anchors(t)); err != nil {
		t.Fatalf("Claim(%s): %v", barrier, err)
	}
}

func (f *watchFixture) advance(t *testing.T, epoch uint64) {
	t.Helper()
	s := unsigned(epoch, "")
	s.SignerCertificate = f.ca.issue(t, f.admin.Public())
	anchors := f.anchors(t)
	anchors.At = testNow
	if err := f.book.Advance(context.Background(), signed(t, f.admin, s), anchors); err != nil {
		t.Fatalf("Advance(%d): %v", epoch, err)
	}
}

func (f *watchFixture) epochs() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uint64, len(f.delivered))
	for i, s := range f.delivered {
		out[i] = s.Epoch
	}
	return out
}

func wantEpochs(t *testing.T, f *watchFixture, want ...uint64) {
	t.Helper()
	got := f.epochs()
	if len(got) != len(want) {
		t.Fatalf("delivered epochs %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delivered epochs %v, want %v", got, want)
		}
	}
}

func TestWatcher_DeliversEachServedEpochOnce(t *testing.T) {
	ctx, f := context.Background(), newWatchFixture(t)
	w := f.watcher(t)
	f.claim(t, clusterseal.TenantBarrier("acme"))
	f.claim(t, clusterseal.TenantBarrier("globex")) // not served here

	if err := w.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if err := w.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	wantEpochs(t, f, 1)

	f.advance(t, 2)
	if err := w.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	wantEpochs(t, f, 1, 2)
}

// A statement that does not verify is never handed to the node: a record the
// platform forged is refused on every honest node.
func TestWatcher_RefusesAStatementThatDoesNotVerify(t *testing.T) {
	ctx, f := context.Background(), newWatchFixture(t)
	w := f.watcher(t)
	forged := signed(t, newKey(t), unsigned(1, ""))
	f.kv.set(Prefix+string(forged.Barrier), mustEncode(t, []InitStatement{forged}))

	wantReason(t, w.Poll(ctx), ReasonWrongSigner)
	wantEpochs(t, f)
}

func TestWatcher_RetriesAFailedHandle(t *testing.T) {
	ctx, f := context.Background(), newWatchFixture(t)
	w := f.watcher(t)
	f.claim(t, clusterseal.TenantBarrier("acme"))
	busy := errors.New("unseal in progress")
	f.handleErr = busy
	if err := w.Poll(ctx); !errors.Is(err, busy) {
		t.Fatalf("Poll = %v, want the handler's error", err)
	}
	f.handleErr = nil
	if err := w.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	wantEpochs(t, f, 1)
}

// A placement table this node cannot read delivers nothing and is retried: the
// node does not initialise a key for a barrier it may not serve.
func TestWatcher_RetriesWhenServesFails(t *testing.T) {
	ctx, f := context.Background(), newWatchFixture(t)
	w := f.watcher(t)
	f.claim(t, clusterseal.TenantBarrier("acme"))
	unreadable := errors.New("placement unreadable")
	f.servesErr = unreadable
	if err := w.Poll(ctx); !errors.Is(err, unreadable) {
		t.Fatalf("Poll = %v, want the placement error", err)
	}
	wantEpochs(t, f)
	f.servesErr = nil
	if err := w.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	wantEpochs(t, f, 1)
}

// A replica added after the init -- by a scale-out or a rebalance move --
// keys itself from the same signed statement on the next poll; no key is
// shipped to it.
func TestWatcher_KeysAReplicaPlacedAfterTheInit(t *testing.T) {
	ctx, f := context.Background(), newWatchFixture(t)
	w := f.watcher(t)
	f.claim(t, clusterseal.TenantBarrier("globex"))
	if err := w.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	wantEpochs(t, f)
	f.placedLater = true
	if err := w.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	wantEpochs(t, f, 1)
}

func TestWatcher_ACorruptRecordDoesNotHideTheOthers(t *testing.T) {
	ctx, f := context.Background(), newWatchFixture(t)
	cfg := f.config(t)
	cfg.Serves = func(context.Context, clusterseal.Barrier) (bool, error) { return true, nil }
	w, err := NewWatcher(cfg)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	f.kv.set(Prefix+string(clusterseal.TenantBarrier("globex")), []byte("{"))
	f.kv.set(Prefix+"not-a-barrier", []byte("{}"))
	f.claim(t, clusterseal.TenantBarrier("acme"))

	var corrupt *CorruptRecordError
	if err := w.Poll(ctx); !errors.As(err, &corrupt) {
		t.Fatalf("Poll = %v, want *CorruptRecordError", err)
	}
	wantEpochs(t, f, 1)
}

func TestWatcher_AnchorAndStoreFailuresAreReported(t *testing.T) {
	ctx, f := context.Background(), newWatchFixture(t)
	f.claim(t, clusterseal.TenantBarrier("acme"))
	cfg := f.config(t)
	noAnchor := errors.New("tenant CA unavailable")
	cfg.Anchors = func(context.Context, clusterseal.Barrier) (Anchors, error) { return Anchors{}, noAnchor }
	w, err := NewWatcher(cfg)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if err := w.Poll(ctx); !errors.Is(err, noAnchor) {
		t.Fatalf("Poll = %v, want the anchor error", err)
	}

	f.kv.fail(errors.New("unavailable"))
	var storeErr *StoreError
	if err := f.watcher(t).Poll(ctx); !errors.As(err, &storeErr) {
		t.Fatalf("Poll on a failing store = %v, want *StoreError", err)
	}
	wantEpochs(t, f)
}

func TestWatcher_RunPollsOnTheClock(t *testing.T) {
	f := newWatchFixture(t)
	cfg := f.config(t)
	cfg.PollInterval = time.Minute
	w, err := NewWatcher(cfg)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	f.claim(t, clusterseal.TenantBarrier("acme"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	wait, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := f.clock.BlockUntil(wait, 1); err != nil {
		t.Fatalf("Run never armed its ticker: %v", err)
	}
	// The first poll runs before the ticker is armed.
	if epoch := <-f.notify; epoch != 1 {
		t.Fatalf("first poll delivered epoch %d, want 1", epoch)
	}

	f.advance(t, 2)
	f.clock.Advance(time.Minute)
	select {
	case epoch := <-f.notify:
		if epoch != 2 {
			t.Fatalf("tick delivered epoch %d, want 2", epoch)
		}
	case <-wait.Done():
		t.Fatal("epoch 2 was not delivered after a tick")
	}
	wantEpochs(t, f, 1, 2)

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v after cancel, want nil", err)
	}
}

func TestNewWatcher_RequiresItsSeams(t *testing.T) {
	f := newWatchFixture(t)
	cases := map[string]func(*WatcherConfig){
		"kv":      func(c *WatcherConfig) { c.KV = nil },
		"anchors": func(c *WatcherConfig) { c.Anchors = nil },
		"serves":  func(c *WatcherConfig) { c.Serves = nil },
		"handle":  func(c *WatcherConfig) { c.Handle = nil },
	}
	for field, mutate := range cases {
		t.Run(field, func(t *testing.T) {
			cfg := f.config(t)
			mutate(&cfg)
			var cfgErr *ConfigError
			if _, err := NewWatcher(cfg); !errors.As(err, &cfgErr) {
				t.Fatalf("NewWatcher without %s = %v, want *ConfigError", field, err)
			}
		})
	}
}
