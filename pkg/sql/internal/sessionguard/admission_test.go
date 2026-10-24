//go:build qrdb_sql

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

package sessionguard

import (
	"context"
	"sync"
	"testing"
	"time"
)

// revocations is an Admission that revokes the tenants it was told to and
// counts how often it is asked about each.
type revocations struct {
	mu      sync.Mutex
	revoked map[string]bool
	asked   map[string]int
}

var _ Admission = (*revocations)(nil)

func newRevocations() *revocations {
	return &revocations{revoked: map[string]bool{}, asked: map[string]int{}}
}

func (r *revocations) Revoked(_ context.Context, tenantID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked[tenantID]++
	return r.revoked[tenantID]
}

func (r *revocations) revoke(tenantID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked[tenantID] = true
}

func (r *revocations) askedAbout(tenantID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.asked[tenantID]
}

// TestEndRevoked_ClosesOnlyTheRevokedTenantsSessions: a revoked tenant's
// connections are all closed, another tenant's stay open, and the admission
// is asked once per tenant, not once per connection.
func TestEndRevoked_ClosesOnlyTheRevokedTenantsSessions(t *testing.T) {
	adm := newRevocations()
	g := New(nil, nil, time.Hour, WithAdmission(adm))
	acme1, acme2, globex := &closeCounter{}, &closeCounter{}, &closeCounter{}
	for tenantID, c := range map[string]*closeCounter{"acme": acme1, "globex": globex} {
		if _, err := g.Open(tenantID, c.close); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Open("acme", acme2.close); err != nil {
		t.Fatal(err)
	}

	g.endRevoked(context.Background())
	if acme1.n.Load()+acme2.n.Load()+globex.n.Load() != 0 {
		t.Fatal("an admitted tenant's connection was closed")
	}

	adm.revoke("acme")
	g.endRevoked(context.Background())
	if acme1.n.Load() != 1 || acme2.n.Load() != 1 {
		t.Fatalf("revoked tenant's connections closed %d and %d times, want 1 each", acme1.n.Load(), acme2.n.Load())
	}
	if globex.n.Load() != 0 {
		t.Fatal("another tenant's revocation closed the connection")
	}
	if got := adm.askedAbout("acme"); got != 2 {
		t.Fatalf("admission asked about acme %d times over two rounds, want 2", got)
	}
}

// TestEndRevoked_NoAdmissionClosesNothing: a guard built without an
// admission never closes a connection for its tenant.
func TestEndRevoked_NoAdmissionClosesNothing(t *testing.T) {
	g := New(nil, nil, time.Hour)
	c := &closeCounter{}
	if _, err := g.Open("acme", c.close); err != nil {
		t.Fatal(err)
	}
	g.endRevoked(context.Background())
	if c.n.Load() != 0 {
		t.Fatal("a guard with no admission closed a connection")
	}
}

// TestEndRevoked_ReleasedSessionNotAsked: a released session is forgotten,
// so its tenant is not asked about and its connection is not closed.
func TestEndRevoked_ReleasedSessionNotAsked(t *testing.T) {
	adm := newRevocations()
	adm.revoke("acme")
	g := New(nil, nil, time.Hour, WithAdmission(adm))
	c := &closeCounter{}
	s, err := g.Open("acme", c.close)
	if err != nil {
		t.Fatal(err)
	}
	s.Release()
	g.endRevoked(context.Background())
	if c.n.Load() != 0 || adm.askedAbout("acme") != 0 {
		t.Fatalf("released session: closed %d times, admission asked %d times", c.n.Load(), adm.askedAbout("acme"))
	}
}

// TestWatch_AdmissionWithoutProbeClosesRevokedTenant: a guard with an
// admission and no seal probe still runs its watch, and closes the
// connections of a tenant revoked while they are open.
func TestWatch_AdmissionWithoutProbeClosesRevokedTenant(t *testing.T) {
	adm := newRevocations()
	g := New(nil, nil, time.Millisecond, WithAdmission(adm))
	g.Start()
	t.Cleanup(g.Stop)
	c := &closeCounter{}
	if _, err := g.Open("acme", c.close); err != nil {
		t.Fatal(err)
	}
	adm.revoke("acme")
	waitClosed(t, c)
}

// TestWatch_SealedProbeEndsAllWithoutAskingAdmission: while the probe reports
// sealed every connection is closed by the seal, and the admission is never
// consulted: a sealed node reads no tenant record.
func TestWatch_SealedProbeEndsAllWithoutAskingAdmission(t *testing.T) {
	adm := newRevocations()
	p := &testProbe{}
	g := New(p, nil, time.Millisecond, WithAdmission(adm))
	c := &closeCounter{}
	if _, err := g.Open("acme", c.close); err != nil {
		t.Fatal(err)
	}
	// Sealed before the watch starts, so no tick ever sees it open.
	p.sealed.Store(true)
	g.Start()
	t.Cleanup(g.Stop)
	waitClosed(t, c)
	if got := adm.askedAbout("acme"); got != 0 {
		t.Fatalf("admission asked %d times while sealed, want 0", got)
	}
}

// blockingAdmission is an Admission whose read never answers on its own: it
// reports entering, then blocks until release is closed or its context is
// done, and reports which of the two ended it.
type blockingAdmission struct {
	entered    chan struct{}
	enterOnce  sync.Once
	release    chan struct{}
	cancelled  chan struct{}
	cancelOnce sync.Once
}

func newBlockingAdmission() *blockingAdmission {
	return &blockingAdmission{
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
}

func (b *blockingAdmission) Revoked(ctx context.Context, _ string) bool {
	b.enterOnce.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		b.cancelOnce.Do(func() { close(b.cancelled) })
	}
	return false
}

// chanCloser returns a session closer and the channel it closes.
func chanCloser() (func(), <-chan struct{}) {
	closed := make(chan struct{})
	return func() { close(closed) }, closed
}

// await fails the test unless ch is closed within the bound. The bound only
// turns a hang into a failure; nothing waits it out on the passing path.
func await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen", what)
	}
}

// An admission read that never answers holds neither the seal nor Stop: the
// probe still closes every session once the node seals, and Stop cancels the
// read instead of waiting for it, without the read ever being released.
func TestWatch_BlockedAdmissionDoesNotDelaySealOrStop(t *testing.T) {
	adm := newBlockingAdmission()
	p := &testProbe{}
	g := New(p, nil, time.Millisecond, WithAdmission(adm))
	closer, closed := chanCloser()
	if _, err := g.Open("acme", closer); err != nil {
		t.Fatal(err)
	}
	g.Start()
	await(t, adm.entered, "the admission read")

	p.sealed.Store(true)
	await(t, closed, "closing the session on seal while the admission read blocks")

	stopped := make(chan struct{})
	go func() {
		g.Stop()
		close(stopped)
	}()
	await(t, stopped, "Stop returning while the admission read blocks")
	select {
	case <-adm.cancelled:
	default:
		t.Fatal("Stop returned without cancelling the admission read")
	}
}

// With the probe reporting unsealed, an admission read that answers revoked
// closes that tenant's sessions and no other tenant's.
func TestWatch_RevokedTenantClosedWhileProbeOpen(t *testing.T) {
	adm := newRevocations()
	adm.revoke("acme")
	g := New(&testProbe{}, nil, time.Millisecond, WithAdmission(adm))
	acmeCloser, acmeClosed := chanCloser()
	if _, err := g.Open("acme", acmeCloser); err != nil {
		t.Fatal(err)
	}
	otherCloser, otherClosed := chanCloser()
	if _, err := g.Open("globex", otherCloser); err != nil {
		t.Fatal(err)
	}
	g.Start()
	await(t, acmeClosed, "closing the revoked tenant's session")
	g.Stop()
	select {
	case <-otherClosed:
		t.Fatal("an admitted tenant's session was closed")
	default:
	}
}

// A cancelled context ends the sweep before any tenant is asked about.
func TestEndRevoked_CancelledContextAsksNothing(t *testing.T) {
	adm := newRevocations()
	adm.revoke("acme")
	g := New(nil, nil, time.Millisecond, WithAdmission(adm))
	closer, closed := chanCloser()
	if _, err := g.Open("acme", closer); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.endRevoked(ctx)
	if got := adm.askedAbout("acme"); got != 0 {
		t.Fatalf("admission asked %d times under a cancelled context, want 0", got)
	}
	select {
	case <-closed:
		t.Fatal("a session was closed under a cancelled context")
	default:
	}
}
