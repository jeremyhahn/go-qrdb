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
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// DefaultPollInterval is how often a Watcher reads the init records.
const DefaultPollInterval = time.Second

// WatcherConfig configures a Watcher. KV, Anchors, Serves and Handle are
// required.
type WatcherConfig struct {
	// KV is the system store the records are read from.
	KV KV
	// Anchors returns what this node trusts a barrier's owner through: the
	// pending first admin's thumbprint and the owner's pinned CA.
	Anchors func(ctx context.Context, b clusterseal.Barrier) (Anchors, error)
	// Serves reports whether this node serves b -- whether b's barrier group
	// has a replica placed here. An error is retried on the next poll.
	Serves func(ctx context.Context, b clusterseal.Barrier) (bool, error)
	// Handle initialises this node's own key for b from a verified statement.
	// It must be idempotent: an error is retried on the next poll.
	Handle func(ctx context.Context, s InitStatement) error
	// PollInterval overrides DefaultPollInterval.
	PollInterval time.Duration
	// Clock times the polls; nil is the real clock.
	Clock clock.Clock
	// Logger overrides slog.Default.
	Logger *slog.Logger
}

// Watcher delivers, on one node, each epoch of every served barrier's init
// statement that verifies, once, to Handle. A statement that does not verify
// is never delivered: a record the platform or one compromised node wrote is
// refused here as on every honest node.
type Watcher struct {
	cfg WatcherConfig

	mu sync.Mutex
	// delivered is the highest epoch Handle accepted per barrier.
	delivered map[clusterseal.Barrier]uint64
}

// NewWatcher returns a Watcher, or *ConfigError for a missing seam.
func NewWatcher(cfg WatcherConfig) (*Watcher, error) {
	switch {
	case cfg.KV == nil:
		return nil, &ConfigError{Field: "KV"}
	case cfg.Anchors == nil:
		return nil, &ConfigError{Field: "Anchors"}
	case cfg.Serves == nil:
		return nil, &ConfigError{Field: "Serves"}
	case cfg.Handle == nil:
		return nil, &ConfigError{Field: "Handle"}
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Watcher{cfg: cfg, delivered: map[clusterseal.Barrier]uint64{}}, nil
}

// Run polls until ctx ends, the first poll at once.
func (w *Watcher) Run(ctx context.Context) error {
	ticker := w.cfg.Clock.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := w.Poll(ctx); err != nil {
			w.cfg.Logger.Warn("barrier init: poll incomplete; retrying", slog.Any("error", err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C():
		}
	}
}

// Poll reads every init record once and delivers each served barrier's latest
// epoch that has not been delivered and verifies. One barrier's failure does
// not hold up another's; every failure is returned, joined.
func (w *Watcher) Poll(ctx context.Context) error {
	raw, err := w.cfg.KV.Scan(ctx, Prefix)
	if err != nil {
		return &StoreError{Op: "scan init records", Err: err}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var errs []error
	for key, val := range raw {
		if err := w.deliver(ctx, key, val); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Deliver hands b's latest statement to Handle now, as the next poll would:
// the same placement filter, verification and epoch bookkeeping apply, so a
// statement already delivered is not delivered twice. It lets a node answer a
// request about b with the state every node reaches eventually, rather than
// with whatever its last poll left. A barrier with no record delivers nothing.
func (w *Watcher) Deliver(ctx context.Context, b clusterseal.Barrier) error {
	key := recordKey(b)
	raw, found, err := w.cfg.KV.Get(ctx, key)
	if err != nil {
		return &StoreError{Op: "read init record", Err: err}
	}
	if !found {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deliver(ctx, key, raw)
}

// deliver handles one record; w.mu is held.
func (w *Watcher) deliver(ctx context.Context, key string, raw []byte) error {
	b, err := clusterseal.ParseBarrier(strings.TrimPrefix(key, Prefix))
	if err != nil {
		return &CorruptRecordError{Key: key, Reason: "key names no barrier"}
	}
	serves, err := w.cfg.Serves(ctx, b)
	if err != nil {
		return err
	}
	if !serves {
		return nil
	}
	versions, err := decodeRecord(key, b, raw)
	if err != nil {
		return err
	}
	latest := versions[len(versions)-1]
	if latest.Epoch <= w.delivered[b] {
		return nil
	}
	anchors, err := w.cfg.Anchors(ctx, b)
	if err != nil {
		return err
	}
	if anchors.At.IsZero() {
		anchors.At = w.cfg.Clock.Now()
	}
	if err := Verify(latest, anchors); err != nil {
		w.cfg.Logger.Error("barrier init: refusing a statement that does not verify",
			slog.String("barrier", string(b)), slog.Uint64("epoch", latest.Epoch), slog.Any("error", err))
		return err
	}
	if err := w.cfg.Handle(ctx, latest); err != nil {
		return err
	}
	w.delivered[b] = latest.Epoch
	return nil
}
