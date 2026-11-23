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
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

type testProbe struct{ sealed atomic.Bool }

func (p *testProbe) IsSealed() bool { return p.sealed.Load() }

// closeCounter counts closes of one connection; its closed channel is
// closed on the first close so a test can wait for it. The zero value is
// usable: the channel is made on first use.
type closeCounter struct {
	n         atomic.Int32
	initOnce  sync.Once
	closeOnce sync.Once
	ch        chan struct{}
}

func newCloseCounter() *closeCounter {
	c := &closeCounter{}
	c.closed()
	return c
}

// closed returns the channel closed by the first close.
func (c *closeCounter) closed() chan struct{} {
	c.initOnce.Do(func() { c.ch = make(chan struct{}) })
	return c.ch
}

func (c *closeCounter) close() {
	c.n.Add(1)
	c.closeOnce.Do(func() { close(c.closed()) })
}

func waitClosed(t *testing.T, c *closeCounter) {
	t.Helper()
	select {
	case <-c.closed():
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was not closed")
	}
}

func newStarted(t *testing.T, probe Probe, streams *clusterseal.Streams) *Guard {
	t.Helper()
	g := New(probe, streams, time.Millisecond)
	g.Start()
	t.Cleanup(g.Stop)
	return g
}

// callerProbe is a sealed probe that counts the reads each watch loop makes,
// telling them apart by the calling function.
type callerProbe struct{ byProbe, byAdmission atomic.Int32 }

func (p *callerProbe) IsSealed() bool {
	if pc, _, _, ok := runtime.Caller(1); ok {
		if fn := runtime.FuncForPC(pc); fn != nil {
			switch {
			case strings.HasSuffix(fn.Name(), ".watchAdmission"):
				p.byAdmission.Add(1)
			case strings.HasSuffix(fn.Name(), ".watchProbe"):
				p.byProbe.Add(1)
			}
		}
	}
	return true
}

// countingAdmission counts every Revoked call.
type countingAdmission struct{ n atomic.Int32 }

func (a *countingAdmission) Revoked(context.Context, string) bool {
	a.n.Add(1)
	return false
}

// TestWatchAdmission_SealedSkipsTheAdmission drives both watch loops
// against a sealed probe until each has read it several times: the
// admission loop skips its tick, so the admission is never consulted.
func TestWatchAdmission_SealedSkipsTheAdmission(t *testing.T) {
	probe := &callerProbe{}
	admission := &countingAdmission{}
	g := New(probe, nil, time.Millisecond, WithAdmission(admission))
	g.Start()
	// Stop is idempotent: deferred so a failed poll still stops both loops.
	defer g.Stop()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.GreaterOrEqual(c, probe.byAdmission.Load(), int32(3), "probe reads by the admission loop")
		assert.GreaterOrEqual(c, probe.byProbe.Load(), int32(3), "probe reads by the probe loop")
	}, 5*time.Second, time.Millisecond, "watch loops did not read the probe often enough")
	g.Stop()
	if n := admission.n.Load(); n != 0 {
		t.Fatalf("a sealed node consulted the admission %d times", n)
	}
}

// TestOpen_RefusesWhileSealed: no session opens while the probe is closed,
// and nothing is left registered.
func TestOpen_RefusesWhileSealed(t *testing.T) {
	for name, streams := range map[string]*clusterseal.Streams{"with streams": clusterseal.NewStreams(), "probe only": nil} {
		t.Run(name, func(t *testing.T) {
			p := &testProbe{}
			p.sealed.Store(true)
			g := newStarted(t, p, streams)
			var sealed *SealedError
			if _, err := g.Open("acme", func() {}); !errors.As(err, &sealed) {
				t.Fatalf("Open while sealed = %v, want *SealedError", err)
			}
			if g.Len() != 0 || (streams != nil && streams.Len() != 0) {
				t.Fatal("a refused open stayed registered")
			}
		})
	}
}

// TestProbeCloseEndsEverySession: a node-local seal, which ends no stream,
// still closes every open connection through the probe watch.
func TestProbeCloseEndsEverySession(t *testing.T) {
	p := &testProbe{}
	g := newStarted(t, p, nil)
	a, b := newCloseCounter(), newCloseCounter()
	if _, err := g.Open("acme", a.close); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Open("globex", b.close); err != nil {
		t.Fatal(err)
	}
	p.sealed.Store(true)
	waitClosed(t, a)
	waitClosed(t, b)
}

// TestStreamEndClosesOnlyItsTenantsSessions: a cluster seal of one tenant's
// database closes that tenant's connections, whichever database they use,
// and leaves another tenant's open; a node seal closes all.
func TestStreamEndClosesOnlyItsTenantsSessions(t *testing.T) {
	streams := clusterseal.NewStreams()
	g := newStarted(t, &testProbe{}, streams)
	acme, globex := newCloseCounter(), newCloseCounter()
	if _, err := g.Open("acme", acme.close); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Open("globex", globex.close); err != nil {
		t.Fatal(err)
	}
	streams.EndDatabase("acme", "ledger", errors.New("sealed"))
	waitClosed(t, acme)
	time.Sleep(20 * time.Millisecond)
	if globex.n.Load() != 0 {
		t.Fatal("another tenant's seal closed the connection")
	}
	streams.EndAll(errors.New("node sealed"))
	waitClosed(t, globex)
}

// TestRelease_ForgetsTheSession: a released session is no longer closed by a
// seal and leaves no subscription; Release is idempotent.
func TestRelease_ForgetsTheSession(t *testing.T) {
	streams := clusterseal.NewStreams()
	p := &testProbe{}
	g := newStarted(t, p, streams)
	c := newCloseCounter()
	s, err := g.Open("acme", c.close)
	if err != nil {
		t.Fatal(err)
	}
	s.Release()
	s.Release()
	if g.Len() != 0 || streams.Len() != 0 {
		t.Fatalf("after Release: sessions %d, subscriptions %d", g.Len(), streams.Len())
	}
	streams.EndAll(errors.New("sealed"))
	p.sealed.Store(true)
	time.Sleep(20 * time.Millisecond)
	if c.n.Load() != 0 {
		t.Fatal("a released session was closed")
	}
}

// TestOpen_SubscribesBeforeItAsksTheProbe: a seal that lands as the session
// opens -- after the subscription, before the probe is read -- ends it.
func TestOpen_SubscribesBeforeItAsksTheProbe(t *testing.T) {
	streams := clusterseal.NewStreams()
	p := &landingProbe{streams: streams}
	g := New(p, streams, time.Hour) // the probe watch must not be what closes it
	g.Start()
	t.Cleanup(g.Stop)
	c := newCloseCounter()
	if _, err := g.Open("acme", c.close); err != nil {
		return // refused outright: as good as closed
	}
	waitClosed(t, c)
}

// landingProbe answers "serving" once and lands a seal as it answers, as the
// monitor would: gate closed (every later answer "sealed"), then End*.
type landingProbe struct {
	streams *clusterseal.Streams
	asked   atomic.Int32
}

func (p *landingProbe) IsSealed() bool {
	if p.asked.Add(1) == 1 {
		p.streams.EndAll(errors.New("sealed"))
		return false
	}
	return true
}

func TestSealedError(t *testing.T) {
	if (&SealedError{}).Error() == "" {
		t.Fatal("empty message")
	}
	g := New(nil, nil, 0)
	if g.interval != DefaultProbeInterval {
		t.Fatalf("interval = %v, want the default", g.interval)
	}
	g.Start()
	g.Stop()
	g.Stop()
}

// stateProbe reports a seal state through sealedwindow.StateProbe.
type stateProbe struct{ sealed, unconfirmed bool }

func (p stateProbe) IsSealed() bool                        { return p.sealed }
func (p stateProbe) SealState() (sealed, unconfirmed bool) { return p.sealed, p.unconfirmed }

// TestRefusal: an unconfirmed seal state is refused as
// SealStateUnconfirmedError, never as SealedError; a sealed or closed
// probe is SealedError; an open probe, or none, admits.
func TestRefusal(t *testing.T) {
	sealedProbe := &testProbe{}
	sealedProbe.sealed.Store(true)
	for name, tc := range map[string]struct {
		probe           Probe
		want, notWanted string
	}{
		"no probe":           {},
		"open":               {probe: &testProbe{}},
		"closed bool probe":  {probe: sealedProbe, want: "sealed"},
		"barrier sealed":     {probe: stateProbe{sealed: true}, want: "sealed", notWanted: "unconfirmed"},
		"seal unconfirmed":   {probe: stateProbe{sealed: true, unconfirmed: true}, want: "unconfirmed", notWanted: "sealed"},
		"state probe serves": {probe: stateProbe{}},
	} {
		t.Run(name, func(t *testing.T) {
			err := Refusal(tc.probe)
			_, isSealed := errors.AsType[*SealedError](err)
			_, isUnconfirmed := errors.AsType[*clusterseal.SealStateUnconfirmedError](err)
			got := map[string]bool{"sealed": isSealed, "unconfirmed": isUnconfirmed}
			if tc.want == "" && err != nil {
				t.Fatalf("Refusal = %v, want nil", err)
			}
			if tc.want != "" && !got[tc.want] {
				t.Fatalf("Refusal = %v, want the %s error", err, tc.want)
			}
			if tc.notWanted != "" && got[tc.notWanted] {
				t.Fatalf("Refusal = %v, must not be the %s error", err, tc.notWanted)
			}
		})
	}
}

// TestOpen_RefusesUnconfirmed: Open reports the unconfirmed state with its
// own error and registers nothing.
func TestOpen_RefusesUnconfirmed(t *testing.T) {
	streams := clusterseal.NewStreams()
	g := newStarted(t, stateProbe{sealed: true, unconfirmed: true}, streams)
	_, err := g.Open("acme", func() {})
	if _, ok := errors.AsType[*clusterseal.SealStateUnconfirmedError](err); !ok {
		t.Fatalf("Open while unconfirmed = %v, want *clusterseal.SealStateUnconfirmedError", err)
	}
	if g.Len() != 0 || streams.Len() != 0 {
		t.Fatal("a refused open stayed registered")
	}
}
