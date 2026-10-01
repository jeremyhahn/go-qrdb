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

package rowlock

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Session is the lock lease of one client session -- one SQL connection --
// shared by the owners of its successive transactions.
//
// Binding each transaction's records to a lease of its own cost a lease
// grant before its first lock and a revoke after its last: two Raft
// proposals on every pessimistic autocommit statement, whatever it locked.
// A session instead keeps one lease per store it locks in, renewed while the
// session lives, and each owner deletes its own records when it is released.
// What the lease is for is unchanged: it is the session's liveness, so a
// session whose process dies stops renewing and every record it still holds
// goes with the lease when it expires.
//
// A lease no owner has used for one lease TTL is revoked, so an idle or
// abandoned session holds nothing and renews nothing. Close revokes every
// lease at once, for the session's end.
//
// A Session is safe for concurrent use.
type Session struct {
	mu     sync.Mutex
	leases map[string]*sessionLease // by lock-table scope
	now    func() time.Time
}

// sessionLease is one lease of a session: the lease every record its owners
// write in one store is bound to.
type sessionLease struct {
	table *Table
	id    int64

	// users counts owners using the lease, and idleSince is when it last
	// fell to zero; both guarded by Session.mu.
	users     int
	idleSince time.Time

	// lost is set once the lease is known to have ended under the session
	// -- expired, or revoked by the leader's sweep -- so every record bound
	// to it is gone, or about to be.
	lost atomic.Bool

	stop    context.CancelFunc
	stopped chan struct{}
}

// NewSession returns a session holding no lease.
func NewSession() *Session {
	return &Session{leases: make(map[string]*sessionLease), now: time.Now}
}

// NewOwner returns an owner for one transaction of the session: a fresh
// identity whose records are bound to the session's lease.
func (s *Session) NewOwner() *Owner {
	o := newOwner()
	o.session = s
	return o
}

// acquire returns the session's lease in t's store, granting one first if
// the session holds none there, and counts one more user of it.
func (s *Session) acquire(ctx context.Context, t *Table) (*sessionLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.leases[t.scope]; l != nil && !l.lost.Load() {
		l.users++
		return l, nil
	}
	granted, err := t.deps.Lease.Grant(ctx, t.leaseTTL)
	if err != nil {
		return nil, err
	}
	kctx, stop := context.WithCancel(context.Background())
	l := &sessionLease{table: t, id: granted.ID, users: 1, stop: stop, stopped: make(chan struct{})}
	s.leases[t.scope] = l
	go s.keepalive(kctx, l)
	return l, nil
}

// release drops one user of l. clean reports whether the owner deleted every
// record it wrote. When it could not, the lease is retired -- revoked, which
// deletes every record still bound to it -- rather than kept: a record its
// owner failed to delete would otherwise outlive the owner for as long as
// the session renews the lease, instead of for at most its TTL. A lease
// found lost is retired too, so the next owner is granted a live one.
func (s *Session) release(ctx context.Context, l *sessionLease, clean bool) error {
	s.mu.Lock()
	l.users--
	if l.users == 0 {
		l.idleSince = s.now()
	}
	retire := !clean || l.lost.Load()
	if retire {
		s.detachLocked(l)
	}
	s.mu.Unlock()
	if !retire {
		return nil
	}
	l.stop()
	<-l.stopped
	return revoke(ctx, l)
}

// Close revokes every lease the session holds, deleting every record still
// bound to them. It is the session's end; the session may be used again
// afterwards, and grants a new lease when it next locks.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	leases := make([]*sessionLease, 0, len(s.leases))
	for _, l := range s.leases {
		leases = append(leases, l)
		s.detachLocked(l)
	}
	s.mu.Unlock()

	var errs []error
	for _, l := range leases {
		l.stop()
		<-l.stopped
		errs = append(errs, revoke(ctx, l))
	}
	return errors.Join(errs...)
}

// detachLocked removes l from the session, so the next acquire in its store
// grants a fresh lease. Callers hold s.mu.
func (s *Session) detachLocked(l *sessionLease) {
	if s.leases[l.table.scope] == l {
		delete(s.leases, l.table.scope)
	}
}

// keepalive renews l every third of its TTL until it is stopped, found lost,
// or idle -- unused for a whole TTL -- in which case it is revoked here.
func (s *Session) keepalive(ctx context.Context, l *sessionLease) {
	defer close(l.stopped)
	ttl := time.Duration(l.table.leaseTTL) * time.Second
	ticker := time.NewTicker(ttl / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		s.mu.Lock()
		idle := l.users == 0 && s.now().Sub(l.idleSince) >= ttl
		if idle {
			s.detachLocked(l)
		}
		s.mu.Unlock()
		if idle {
			// Nothing is bound to an idle lease; revoking it is cleanup.
			// The keepalive context is the lease's own and is not done.
			_ = revoke(ctx, l)
			return
		}

		if _, err := l.table.deps.Lease.KeepAlive(ctx, l.id); err != nil {
			var gone *service.LeaseNotFoundError
			if errors.As(err, &gone) {
				l.lost.Store(true)
				s.mu.Lock()
				s.detachLocked(l)
				s.mu.Unlock()
				return
			}
			// A transient failure (a leader change, a slow quorum) is
			// retried on the next tick; two misses still leave a third of
			// the TTL.
		}
	}
}

// revoke revokes l, detached from ctx's cancellation and bounded by
// releaseTimeout: the paths that revoke -- a dropped connection, a statement
// past its deadline -- are exactly the ones whose context is already done.
// A lease already gone is not an error.
func revoke(ctx context.Context, l *sessionLease) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := l.table.deps.Lease.Revoke(rctx, l.id); err != nil {
		var gone *service.LeaseNotFoundError
		if errors.As(err, &gone) {
			return nil
		}
		return err
	}
	return nil
}
