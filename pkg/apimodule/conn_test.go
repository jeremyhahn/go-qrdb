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

package apimodule

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// countingObserver is a module that counts connection events per transport.
type countingObserver struct {
	BaseModule
	mu     sync.Mutex
	opened map[Transport]int
	closed map[Transport]int
}

var _ ConnObserver = (*countingObserver)(nil)

func newCountingObserver() *countingObserver {
	return &countingObserver{opened: map[Transport]int{}, closed: map[Transport]int{}}
}

func (o *countingObserver) Name() string { return "counting-observer" }

func (o *countingObserver) ConnOpened(t Transport) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opened[t]++
}

func (o *countingObserver) ConnClosed(t Transport) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed[t]++
}

func (o *countingObserver) counts(t Transport) (opened, closed int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opened[t], o.closed[t]
}

// plainModule observes nothing.
type plainModule struct{ BaseModule }

func (plainModule) Name() string { return "plain" }

// pipeListener is an in-memory net.Listener: Accept hands out the server
// ends of net.Pipe pairs queued by dial, so these tests open no socket.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn, 16), closed: make(chan struct{})}
}

func (l *pipeListener) dial() net.Conn {
	server, client := net.Pipe()
	l.conns <- server
	return client
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func TestConnObserversOf_CollectsOnlyObservers(t *testing.T) {
	a, b := newCountingObserver(), newCountingObserver()
	got := ConnObserversOf([]ServiceModule{plainModule{}, a, plainModule{}, b})
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("ConnObserversOf = %v, want [a b] in registration order", got)
	}
	if got := ConnObserversOf([]ServiceModule{plainModule{}}); len(got) != 0 {
		t.Fatalf("no observer modules gave %d observers", len(got))
	}
	if got := ConnObserversOf(nil); len(got) != 0 {
		t.Fatalf("nil modules gave %d observers", len(got))
	}
}

// Open reports to every observer, and the close it returns reports exactly
// once however often a connection's close paths call it.
func TestConnObservers_OpenReportsOnceEach(t *testing.T) {
	a, b := newCountingObserver(), newCountingObserver()
	obs := ConnObservers{a, b}

	closed := obs.Open(TransportQUIC)
	for _, o := range []*countingObserver{a, b} {
		if opened, c := o.counts(TransportQUIC); opened != 1 || c != 0 {
			t.Fatalf("after Open: opened=%d closed=%d, want 1/0", opened, c)
		}
	}
	closed()
	closed()
	for _, o := range []*countingObserver{a, b} {
		if opened, c := o.counts(TransportQUIC); opened != 1 || c != 1 {
			t.Fatalf("after two closes: opened=%d closed=%d, want 1/1", opened, c)
		}
	}
	if opened, _ := a.counts(TransportMCP); opened != 0 {
		t.Error("an event leaked onto another transport")
	}
}

// The zero value costs nothing: Open is a no-op and Listener hands back the
// very listener it was given, so a server with no observing module accepts
// exactly as it did before the hook existed.
func TestConnObservers_ZeroValueIsANoOp(t *testing.T) {
	var obs ConnObservers
	obs.Open(TransportREST)()

	ln := newPipeListener()
	if got := obs.Listener(ln, TransportREST); got != net.Listener(ln) {
		t.Fatalf("Listener with no observers wrapped the listener: %T", got)
	}
}

// A wrapped listener reports each accepted connection open on Accept and
// closed on its first Close, and passes everything else through.
func TestConnObservers_ListenerReportsAcceptAndClose(t *testing.T) {
	o := newCountingObserver()
	raw := newPipeListener()
	ln := ConnObservers{o}.Listener(raw, TransportMCP)
	if ln.Addr().String() != "pipe" {
		t.Fatalf("Addr not delegated: %v", ln.Addr())
	}

	client := raw.dial()
	defer client.Close()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if opened, closed := o.counts(TransportMCP); opened != 1 || closed != 0 {
		t.Fatalf("after Accept: opened=%d closed=%d, want 1/0", opened, closed)
	}

	// The connection still carries data in both directions.
	go func() { _, _ = client.Write([]byte("ping")) }()
	buf := make([]byte, 4)
	if _, err := conn.Read(buf); err != nil || string(buf) != "ping" {
		t.Fatalf("Read through the wrapper = %q, %v", buf, err)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = conn.Close() // a second close path must not report again
	if opened, closed := o.counts(TransportMCP); opened != 1 || closed != 1 {
		t.Fatalf("after Close: opened=%d closed=%d, want 1/1", opened, closed)
	}

	if err := ln.Close(); err != nil {
		t.Fatalf("listener Close: %v", err)
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close = %v, want net.ErrClosed", err)
	}
	if opened, _ := o.counts(TransportMCP); opened != 1 {
		t.Errorf("a failed Accept was reported as a connection: opened=%d", opened)
	}
}

// Connections opened and closed concurrently are counted exactly; run under
// -race this is also the check that the hook adds no data race of its own.
func TestConnObservers_ConcurrentConnectionsBalance(t *testing.T) {
	var opened, closed atomic.Int64
	obs := ConnObservers{funcObserver{
		open:  func(Transport) { opened.Add(1) },
		close: func(Transport) { closed.Add(1) },
	}}

	const n = 64
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			done := obs.Open(TransportGRPC)
			done()
			done()
		})
	}
	wg.Wait()
	if opened.Load() != n || closed.Load() != n {
		t.Fatalf("opened=%d closed=%d, want %d each", opened.Load(), closed.Load(), n)
	}
}

// funcObserver adapts two functions to ConnObserver.
type funcObserver struct {
	open, close func(Transport)
}

func (f funcObserver) ConnOpened(t Transport) { f.open(t) }
func (f funcObserver) ConnClosed(t Transport) { f.close(t) }

// handshakeCreds is a TransportCredentials whose server handshake returns
// the raw conn unchanged, or fails with err.
type handshakeCreds struct {
	credentials.TransportCredentials
	err    error
	clones *atomic.Int64
}

var errHandshake = errors.New("handshake refused")

func (h handshakeCreds) ServerHandshake(raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	if h.err != nil {
		return nil, nil, h.err
	}
	return raw, nil, nil
}

func (h handshakeCreds) Clone() credentials.TransportCredentials {
	h.clones.Add(1)
	return h
}

func TestConnObservers_CredentialsZeroValueIsANoOp(t *testing.T) {
	c := insecure.NewCredentials()
	if got := (ConnObservers{}).Credentials(c, TransportGRPC); got != c {
		t.Fatalf("Credentials with no observers wrapped them: %T", got)
	}
}

// The handshaken conn is observed: opened when the handshake starts, closed
// on its first Close. The RAW conn grpc-go accepted is not wrapped — it is
// what the handshake received, untouched.
func TestConnObservers_CredentialsReportHandshakenConn(t *testing.T) {
	o := newCountingObserver()
	var clones atomic.Int64
	creds := ConnObservers{o}.Credentials(handshakeCreds{TransportCredentials: insecure.NewCredentials(), clones: &clones}, TransportUnix)

	server, client := net.Pipe()
	defer client.Close()
	conn, _, err := creds.ServerHandshake(server)
	if err != nil {
		t.Fatalf("ServerHandshake: %v", err)
	}
	if opened, closed := o.counts(TransportUnix); opened != 1 || closed != 0 {
		t.Fatalf("after handshake: opened=%d closed=%d, want 1/0", opened, closed)
	}
	_ = conn.Close()
	_ = conn.Close()
	if opened, closed := o.counts(TransportUnix); opened != 1 || closed != 1 {
		t.Fatalf("after Close: opened=%d closed=%d, want 1/1", opened, closed)
	}

	// Clone keeps observing: grpc-go may clone the credentials it was given.
	cloned := creds.Clone()
	if clones.Load() != 1 {
		t.Fatal("Clone did not clone the wrapped credentials")
	}
	s2, c2 := net.Pipe()
	defer c2.Close()
	conn2, _, err := cloned.ServerHandshake(s2)
	if err != nil {
		t.Fatalf("cloned ServerHandshake: %v", err)
	}
	defer conn2.Close()
	if opened, _ := o.counts(TransportUnix); opened != 2 {
		t.Fatalf("a cloned credential's handshake was not observed: opened=%d", opened)
	}
}

// A connection that fails its handshake was accepted, so it is counted — and
// closed in the same breath, so a gauge built on the events never leaks it.
func TestConnObservers_CredentialsFailedHandshakeBalances(t *testing.T) {
	o := newCountingObserver()
	creds := ConnObservers{o}.Credentials(handshakeCreds{TransportCredentials: insecure.NewCredentials(), err: errHandshake, clones: new(atomic.Int64)}, TransportGRPC)
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	if _, _, err := creds.ServerHandshake(server); !errors.Is(err, errHandshake) {
		t.Fatalf("err = %v, want the handshake's own error", err)
	}
	if opened, closed := o.counts(TransportGRPC); opened != 1 || closed != 1 {
		t.Fatalf("failed handshake: opened=%d closed=%d, want 1/1", opened, closed)
	}
}
