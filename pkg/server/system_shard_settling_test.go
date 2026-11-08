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

package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The ReadIndex warm-up of a freshly elected leader is waited out; any
// other failure, and a cancelled context, is returned at once.
func TestWhileSystemShardSettling(t *testing.T) {
	calls := 0
	err := WhileSystemShardSettling(context.Background(), nil, "test read", func(context.Context) error {
		calls++
		if calls < 3 {
			return quicraft.ErrReadIndexNotReady
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err = %v after %d calls, want nil after 3", err, calls)
	}

	boom := errors.New("boom")
	calls = 0
	err = WhileSystemShardSettling(context.Background(), nil, "test read", func(context.Context) error { calls++; return boom })
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want boom after 1", err, calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	err = WhileSystemShardSettling(ctx, nil, "test read", func(context.Context) error { calls++; return nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("err = %v after %d calls, want context.Canceled before any call", err, calls)
	}
}

// An election in progress -- the system shard has no known leader -- is
// waited out the same way. A node booting while a peer restarts (a rolling
// enable-encryption recycles every node in turn) loses the leader it found
// during provisioning and must not fail on the read that follows. A
// not-leader answer that names a leader is not an election and is returned.
func TestWhileSystemShardSettling_WaitsOutAnElection(t *testing.T) {
	calls := 0
	err := WhileSystemShardSettling(context.Background(), nil, "test read", func(context.Context) error {
		calls++
		if calls < 3 {
			return &service.NotLeaderError{ShardID: 1}
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err = %v after %d calls, want nil after 3", err, calls)
	}

	known := &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "192.0.2.2:5000"}
	calls = 0
	err = WhileSystemShardSettling(context.Background(), nil, "test read", func(context.Context) error { calls++; return known })
	if !errors.Is(err, known) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want the known-leader error after 1", err, calls)
	}
}

// TestWhileSystemShardSettling_WaitsOutAnElection_TransportLayer is the
// embedded-transport proof: a caller one layer removed from the service
// package (the feature gate's self-advertisement, writing through an
// in-process sdk.Client) sees *transport.NotLeaderError, not
// *service.NotLeaderError -- sdk/go/transport/embedded's translateError
// reconstructs a fresh value with no Unwrap chain back to the service-layer
// type -- and must be waited out exactly the same way.
func TestWhileSystemShardSettling_WaitsOutAnElection_TransportLayer(t *testing.T) {
	calls := 0
	err := WhileSystemShardSettling(context.Background(), nil, "test read", func(context.Context) error {
		calls++
		if calls < 3 {
			return &transport.NotLeaderError{ShardID: 1}
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err = %v after %d calls, want nil after 3", err, calls)
	}

	known := &transport.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "192.0.2.2:5000"}
	calls = 0
	err = WhileSystemShardSettling(context.Background(), nil, "test read", func(context.Context) error { calls++; return known })
	if !errors.Is(err, known) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want the known-leader error after 1", err, calls)
	}
}

// A read pending on a follower when the leader changes is failed with a
// not-leader error naming the new leader but no address (go-quicraft's
// leader-change notice), so the caller retries against it. That is the shard
// settling, at either layer: a boot read that gave up on it exited the node
// mid-election, and the exit took the quorum its peers were electing with.
func TestWhileSystemShardSettling_WaitsOutALeaderChange(t *testing.T) {
	for name, notice := range map[string]error{
		"service":   &service.NotLeaderError{ShardID: 1, LeaderID: 1},
		"transport": &transport.NotLeaderError{ShardID: 1, LeaderID: 1},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			err := WhileSystemShardSettling(context.Background(), nil, "test read", func(context.Context) error {
				calls++
				if calls < 3 {
					return notice
				}
				return nil
			})
			if err != nil || calls != 3 {
				t.Fatalf("err = %v after %d calls, want nil after 3", err, calls)
			}
		})
	}
}

// Each attempt is a startup read with its own bound: fn is handed a context
// that ends within StartupReadAttemptTimeout even when the caller's never
// does, so a read in fn that never returns is cut off rather than holding the
// boot.
func TestWhileSystemShardSettling_BoundsEachAttempt(t *testing.T) {
	var left time.Duration
	hasDeadline := false
	err := WhileSystemShardSettling(context.Background(), nil, "test read", func(actx context.Context) error {
		var dl time.Time
		dl, hasDeadline = actx.Deadline()
		left = time.Until(dl)
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !hasDeadline || left <= 0 || left > StartupReadAttemptTimeout {
		t.Fatalf("attempt context deadline: set=%v, %s left; want set and within %s", hasDeadline, left, StartupReadAttemptTimeout)
	}
}

// formingShard is a system shard that has a leader once elected is set, and
// whose leader has committed in its term once committed is set.
type formingShard struct{ elected, committed atomic.Bool }

var _ shardLeaderSource = (*formingShard)(nil)

func (s *formingShard) LeaderID(uint64) (uint64, bool, error) {
	return 1, s.elected.Load(), nil
}

func (s *formingShard) ReadReady(context.Context, uint64) error {
	if s.committed.Load() {
		return nil
	}
	return quicraft.ErrReadIndexNotReady
}

// unanswered is an attempt the system shard drops: it ends only when its
// bound does.
func unanswered(actx context.Context) error {
	<-actx.Done()
	return actx.Err()
}

// A cluster still forming does not spend the budget: attempts cut off
// unanswered for well past SystemShardSettleBudget, while no leader has
// committed in its term, keep the wait open, and the read that a committed
// leader answers ends it.
func TestWhileSystemShardSettling_OutlastsAFormingCluster(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &formingShard{}
		formed := 4 * SystemShardSettleBudget
		go func() {
			time.Sleep(formed / 2)
			s.elected.Store(true)
			time.Sleep(formed / 2)
			s.committed.Store(true)
		}()
		start := time.Now()
		err := WhileSystemShardSettling(context.Background(), s, "test read", func(actx context.Context) error {
			if s.committed.Load() {
				return nil
			}
			return unanswered(actx)
		})
		if err != nil {
			t.Fatalf("err = %v, want nil once a leader has committed", err)
		}
		if elapsed := time.Since(start); elapsed < formed {
			t.Fatalf("returned after %s, before the leader committed at %s", elapsed, formed)
		}
	})
}

// A leader that has committed in its term and still does not answer spends
// the budget, and the wait ends with the startup-read error at the budget.
func TestWhileSystemShardSettling_CommittedLeaderSpendsTheBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &formingShard{}
		s.elected.Store(true)
		s.committed.Store(true)
		start := time.Now()
		err := WhileSystemShardSettling(context.Background(), s, "test read", unanswered)
		var sre *StartupReadError
		if !errors.As(err, &sre) || sre.Cause != nil {
			t.Fatalf("err = %v, want a *StartupReadError at the deadline", err)
		}
		if elapsed := time.Since(start); elapsed < SystemShardSettleBudget || elapsed >= SystemShardSettleBudget+StartupReadAttemptTimeout {
			t.Fatalf("gave up after %s, want at the %s budget", elapsed, SystemShardSettleBudget)
		}
	})
}

// leaderOnly is a LeaderSource that cannot probe its leader's term.
type leaderOnly struct{ known atomic.Bool }

func (l *leaderOnly) LeaderID(uint64) (uint64, bool, error) { return 1, l.known.Load(), nil }

// A leader source without the read probe counts the budget from the moment
// it knows a leader, and not before.
func TestWhileSystemShardSettling_KnownLeaderWithoutProbeCounts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &leaderOnly{}
		elected := 2 * SystemShardSettleBudget
		go func() {
			time.Sleep(elected)
			l.known.Store(true)
		}()
		start := time.Now()
		err := WhileSystemShardSettling(context.Background(), l, "test read", unanswered)
		var sre *StartupReadError
		if !errors.As(err, &sre) || sre.Cause != nil {
			t.Fatalf("err = %v, want a *StartupReadError at the deadline", err)
		}
		if elapsed := time.Since(start); elapsed < elected+SystemShardSettleBudget {
			t.Fatalf("gave up after %s, want the %s budget counted from the election at %s", elapsed, SystemShardSettleBudget, elected)
		}
	})
}

// A forming cluster's wait still ends when its caller gives up.
func TestWhileSystemShardSettling_FormingClusterEndsWithCtx(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*SystemShardSettleBudget)
		defer cancel()
		err := WhileSystemShardSettling(ctx, &formingShard{}, "test read", unanswered)
		var sre *StartupReadError
		if !errors.As(err, &sre) || !errors.Is(sre.Cause, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want a *StartupReadError caused by ctx", err)
		}
	})
}

func TestSystemShardLeaders(t *testing.T) {
	if got := systemShardLeaders(nil); got != nil {
		t.Fatalf("systemShardLeaders(nil) = %#v, want a nil LeaderSource", got)
	}
	if systemShardCommitted(context.Background(), &leaderOnly{}) {
		t.Fatal("a source that knows no leader reported a committed leader")
	}
}
