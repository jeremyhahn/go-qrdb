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

package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync/atomic"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// settlingCatalog is a catalog whose List fails with listErr for its first
// failures calls, as the KV catalog's linearizable scan does while the
// system shard elects a leader or warms up its ReadIndex. Only List is
// reached by OpenAll; any other method panics on the nil embedded catalog.
type settlingCatalog struct {
	database.Catalog
	listErr  error
	failures int
	calls    int
}

func (c *settlingCatalog) List(context.Context) ([]database.DatabaseMeta, error) {
	c.calls++
	if c.calls <= c.failures {
		return nil, c.listErr
	}
	return nil, nil
}

// A boot that lists the catalog while the system shard is still settling
// waits the settling out instead of failing: the readiness wait comes after
// BuildServices, so nothing else has waited for it yet.
func TestOpenDatabaseCatalog_WaitsOutSystemShardSettling(t *testing.T) {
	for name, listErr := range map[string]error{
		"read index not ready": &service.ReadError{Err: quicraft.ErrReadIndexNotReady},
		"no leader yet":        &service.NotLeaderError{ShardID: 1},
	} {
		t.Run(name, func(t *testing.T) {
			m := database.NewManager(slog.New(slog.DiscardHandler), t.TempDir())
			cat := &settlingCatalog{listErr: listErr, failures: 2}
			if err := openDatabaseCatalog(context.Background(), knownLeader(), m, cat); err != nil {
				t.Fatalf("openDatabaseCatalog = %v, want nil once the shard settles", err)
			}
			if cat.calls != 3 {
				t.Fatalf("List called %d times, want 3", cat.calls)
			}
		})
	}
}

// A failure that is not the shard settling still fails the boot at once,
// typed, and a cancelled boot is not retried.
func TestOpenDatabaseCatalog_FailsOnPermanentError(t *testing.T) {
	boom := errors.New("boom")
	m := database.NewManager(slog.New(slog.DiscardHandler), t.TempDir())
	cat := &settlingCatalog{listErr: boom, failures: 1 << 30}
	err := openDatabaseCatalog(context.Background(), knownLeader(), m, cat)
	var catErr *DatabaseCatalogError
	if !errors.As(err, &catErr) || !errors.Is(err, boom) {
		t.Fatalf("openDatabaseCatalog = %v, want *DatabaseCatalogError wrapping boom", err)
	}
	if cat.calls != 1 {
		t.Fatalf("List called %d times, want 1", cat.calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cat = &settlingCatalog{listErr: quicraft.ErrReadIndexNotReady, failures: 1 << 30}
	err = openDatabaseCatalog(ctx, knownLeader(), m, cat)
	if !errors.As(err, &catErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("openDatabaseCatalog = %v, want *DatabaseCatalogError wrapping context.Canceled", err)
	}
	if cat.calls != 0 {
		t.Fatalf("List called %d times after cancel, want 0", cat.calls)
	}
}

// catalogLeader is the system shard's leader as openDatabaseCatalog polls it:
// unknown for the first noLeaderPolls polls, then node 1.
type catalogLeader struct {
	noLeaderPolls int64
	polls         atomic.Int64
}

var _ server.LeaderSource = (*catalogLeader)(nil)

func (c *catalogLeader) LeaderID(shardID uint64) (uint64, bool, error) {
	if shardID != shard.ShardKV {
		return 0, false, errors.New("catalogLeader: polled a shard other than the system shard")
	}
	if c.polls.Add(1) <= c.noLeaderPolls {
		return 0, false, nil
	}
	return 1, true, nil
}

// knownLeader is a system shard whose leader is already known.
func knownLeader() *catalogLeader { return &catalogLeader{} }

// TestOpenDatabaseCatalog_WaitsForTheSystemShardLeader: with the peers still
// sealed the system shard has no leader, and no settling budget can make the
// read succeed. The open waits for the leader first, past any fixed budget,
// and reads the catalog only once one is known.
func TestOpenDatabaseCatalog_WaitsForTheSystemShardLeader(t *testing.T) {
	m := database.NewManager(slog.New(slog.DiscardHandler), t.TempDir())
	leaders := &catalogLeader{noLeaderPolls: 5}
	cat := &settlingCatalog{}
	if err := openDatabaseCatalog(context.Background(), leaders, m, cat); err != nil {
		t.Fatalf("openDatabaseCatalog = %v, want nil once a leader is known", err)
	}
	// Six by the leader wait, one by the settling wait asking whether its
	// budget counts before its first attempt.
	if got := leaders.polls.Load(); got != 7 {
		t.Fatalf("leader polled %d times, want 7", got)
	}
	if cat.calls != 1 {
		t.Fatalf("List called %d times, want 1", cat.calls)
	}
}

// TestOpenDatabaseCatalog_CancelEndsTheLeaderWait: a boot abandoned while the
// system shard has no leader ends with the context's error, typed as the
// catalog open's, and never reads the catalog.
func TestOpenDatabaseCatalog_CancelEndsTheLeaderWait(t *testing.T) {
	m := database.NewManager(slog.New(slog.DiscardHandler), t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leaders := &catalogLeader{noLeaderPolls: 1 << 30}
	go func() {
		for leaders.polls.Load() < 3 {
			runtime.Gosched()
		}
		cancel()
	}()
	cat := &settlingCatalog{}
	err := openDatabaseCatalog(ctx, leaders, m, cat)
	var catErr *DatabaseCatalogError
	if !errors.As(err, &catErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("openDatabaseCatalog = %v, want *DatabaseCatalogError wrapping context.Canceled", err)
	}
	if cat.calls != 0 {
		t.Fatalf("List called %d times with no leader, want 0", cat.calls)
	}
}
