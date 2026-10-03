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
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

type testProbe struct{ sealed atomic.Bool }

func (p *testProbe) IsSealed() bool { return p.sealed.Load() }

// closeCounter counts closes of one connection.
type closeCounter struct{ n atomic.Int32 }

func (c *closeCounter) close() { c.n.Add(1) }

func waitClosed(t *testing.T, c *closeCounter) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.n.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the connection was not closed")
		}
		time.Sleep(time.Millisecond)
	}
}

func newStarted(t *testing.T, probe Probe, streams *clusterseal.Streams) *Guard {
	t.Helper()
	g := New(probe, streams, time.Millisecond)
	g.Start()
	t.Cleanup(g.Stop)
	return g
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
	a, b := &closeCounter{}, &closeCounter{}
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
	acme, globex := &closeCounter{}, &closeCounter{}
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
	c := &closeCounter{}
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
	c := &closeCounter{}
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
