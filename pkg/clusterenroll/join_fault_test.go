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

// Fault-injection tests for handle (the join listener's responder side) and
// the initiator exchanges, joinClientExchange and joinClientExchangeP256. All
// take a net.Conn precisely so a test can fail a specific Read or Write
// deterministically -- see their own doc comments -- rather than racing real
// socket timing to reach a mid-exchange peer-close or frame-failure branch.
// This file is that test: a faultyConn wraps one real net.Pipe() end and fails
// Read or Write from a configured call number onward, while the other end runs
// the real counterpart (a genuine session of the suite this runtime selects --
// Noise_NK, or P-256 under FIPS 140-3 mode), so the failure lands exactly on
// the frame it targets rather than approximately.
package clusterenroll

import (
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/flynn/noise"
	"github.com/jeremyhahn/go-truststrap/pkg/noiseproto"
	noiseboot "github.com/jeremyhahn/go-truststrap/pkg/noiseproto/bootstrap"
	"github.com/jeremyhahn/go-truststrap/pkg/p256proto"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// errInjectedFault is what faultyConn returns in place of the real Read or
// Write once the configured call count is reached.
var errInjectedFault = errors.New("clusterenroll test: injected fault")

// faultyConn wraps a net.Conn and fails Read and/or Write from a configured
// call number onward (1-indexed; 0 fails every call, -1 never fails). It
// never partially services a call it is about to fail: the failure happens
// before the underlying Read/Write runs, so a counterpart blocked on the
// matching rendezvous unblocks only when the fault side closes the
// connection, exactly as a real mid-exchange peer close would.
type faultyConn struct {
	net.Conn
	failReadAfter  int
	failWriteAfter int
	reads          int
	writes         int
}

func (c *faultyConn) Read(p []byte) (int, error) {
	c.reads++
	if c.failReadAfter >= 0 && c.reads > c.failReadAfter {
		return 0, errInjectedFault
	}
	return c.Conn.Read(p)
}

func (c *faultyConn) Write(p []byte) (int, error) {
	c.writes++
	if c.failWriteAfter >= 0 && c.writes > c.failWriteAfter {
		return 0, errInjectedFault
	}
	return c.Conn.Write(p)
}

// newTestJoinServer builds a *JoinServer directly, without a real listener,
// for driving handle against a net.Pipe() end. It carries its own throwaway
// token store and trust domain: every fault test in this file targets a
// transport-level failure (a Read/Write that never completes), which is
// reached before -- or regardless of -- whether the presented token is
// valid, so none of them need a token actually minted into it. Like
// StartJoinServer it always holds a P-256 key and holds a Noise key only
// where this runtime may use Noise.
func newTestJoinServer(t *testing.T, provider cakey.Provider) *JoinServer {
	t.Helper()
	p256Key, err := p256proto.GenerateStaticKey()
	if err != nil {
		t.Fatalf("p256proto.GenerateStaticKey: %v", err)
	}
	t.Cleanup(p256Key.Destroy)
	js := &JoinServer{
		p256Key:     p256Key,
		store:       kvstore.NewMemoryKVStore(),
		trustDomain: testTrustDomain,
		provider:    provider,
		logger:      discardLogger(),
	}
	if NoiseAvailable() {
		key, err := noiseproto.GenerateStaticKey()
		if err != nil {
			t.Fatalf("noiseproto.GenerateStaticKey: %v", err)
		}
		t.Cleanup(key.Destroy)
		js.key = key
	}
	return js
}

// joinClientExchangeFunc is the shape both suites' initiator exchanges share.
type joinClientExchangeFunc func(net.Conn, []byte, time.Duration, string, []byte, string, uint64, []string) ([]byte, error)

// testJoinClient returns the initiator exchange, and the server key it pins,
// for the suite a dual-key client selects against js in this runtime: Noise
// where it is available, P-256 under FIPS 140-3 mode. It goes through
// selectJoinSuite, so the choice is JoinCSR's own.
func testJoinClient(t *testing.T, js *JoinServer) (joinClientExchangeFunc, []byte) {
	t.Helper()
	suite, key, err := selectJoinSuite(&JoinClientOptions{ServerKey: js.PublicKey(), ServerP256Key: js.P256PublicKey()})
	if err != nil {
		t.Fatalf("selectJoinSuite: %v", err)
	}
	return joinClientSuites[suite], key
}

// rawClientStop names the point at which rawJoinClient stops playing the
// initiator.
type rawClientStop int

const (
	// stopAfterMsg1 sends handshake message 1 and stops.
	stopAfterMsg1 rawClientStop = iota + 1
	// stopAfterRequest completes the handshake, sends the encrypted request and
	// stops.
	stopAfterRequest
	// stopAfterResponse also reads the response frame.
	stopAfterResponse
)

// rawJoinClient plays the initiator against js over conn, in the suite this
// runtime selects, as far as stop. Each first frame opens with the
// version||suite header handle dispatches on, so the server reaches the frame a
// caller faults rather than refusing the first one. It never calls t.Fatalf:
// it runs in a goroutine in every caller, where FailNow is unsafe, and a step
// that fails simply ends it.
func rawJoinClient(conn net.Conn, js *JoinServer, request []byte, stop rawClientStop) {
	deadline := time.Now().Add(5 * time.Second)
	var (
		msg1   []byte
		finish func(msg2 []byte) (joinChannel, error)
	)
	if NoiseAvailable() {
		header := noiseboot.NoiseSuiteHeader()
		sess, err := noiseproto.NewSession(&noiseproto.SessionConfig{
			Pattern:       noise.HandshakeNK,
			PeerStaticKey: js.PublicKey(),
			IsInitiator:   true,
			Prologue:      header,
		})
		if err != nil {
			return
		}
		defer sess.Destroy()
		if err := sess.InitHandshake(); err != nil {
			return
		}
		m1, _, err := sess.HandshakeMessage(nil)
		if err != nil {
			return
		}
		msg1 = append(header, m1...)
		finish = func(msg2 []byte) (joinChannel, error) {
			if _, done, err := sess.HandshakeMessage(msg2); err != nil || !done {
				return nil, ErrHandshakeIncomplete
			}
			return sess, nil
		}
	} else {
		ini, err := p256proto.NewInitiator(js.P256PublicKey())
		if err != nil {
			return
		}
		defer ini.Destroy()
		if msg1, err = ini.WriteMessage1(); err != nil {
			return
		}
		finish = func(msg2 []byte) (joinChannel, error) {
			transport, err := ini.ReadMessage2(msg2)
			if err != nil {
				return nil, err
			}
			return &p256Channel{t: transport}, nil
		}
	}

	if err := noiseboot.WriteFrame(conn, msg1, deadline); err != nil || stop == stopAfterMsg1 {
		return
	}
	msg2, err := noiseboot.ReadFrame(conn, deadline)
	if err != nil {
		return
	}
	ch, err := finish(msg2)
	if err != nil {
		return
	}
	ciphertext, err := ch.Encrypt(request)
	if err != nil {
		return
	}
	if err := noiseboot.WriteFrame(conn, ciphertext, deadline); err != nil || stop == stopAfterRequest {
		return
	}
	_, _ = noiseboot.ReadFrame(conn, deadline)
}

// TestHandle_ReadMsg1Fails exercises handle's "read handshake message 1
// failed" branch: the very first Read on the server's conn fails, before any
// data has moved in either direction.
func TestHandle_ReadMsg1Fails(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	js := newTestJoinServer(t, provider)

	_, serverConn := net.Pipe()
	fc := &faultyConn{Conn: serverConn, failReadAfter: 0, failWriteAfter: -1}

	done := make(chan struct{})
	go func() {
		js.handle(fc)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handle did not return after its first read was faulted")
	}
}

// TestHandle_WriteMsg2Fails exercises handle's "write handshake message 2
// failed" branch: the server reads a genuine message 1, then its first Write
// (message 2) fails.
func TestHandle_WriteMsg2Fails(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	js := newTestJoinServer(t, provider)

	clientConn, serverConn := net.Pipe()
	fc := &faultyConn{Conn: serverConn, failReadAfter: -1, failWriteAfter: 0}

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		rawJoinClient(clientConn, js, nil, stopAfterMsg1)
	}()

	done := make(chan struct{})
	go func() {
		js.handle(fc)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handle did not return after its first write was faulted")
	}
	<-clientDone
}

// TestHandle_ReadRequestFails exercises handle's "read request failed"
// branch: a genuine handshake completes (both of the server's writes
// succeed), then the server's next Read (the encrypted request frame) fails.
func TestHandle_ReadRequestFails(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	js := newTestJoinServer(t, provider)

	clientConn, serverConn := net.Pipe()
	// Two reads (header+payload) complete handshake message 1; the third
	// read -- the request frame's header -- is where this must fail.
	fc := &faultyConn{Conn: serverConn, failReadAfter: 2, failWriteAfter: -1}

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		rawJoinClient(clientConn, js, []byte(`{"token":"tok"}`), stopAfterRequest)
	}()

	done := make(chan struct{})
	go func() {
		js.handle(fc)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handle did not return after its request read was faulted")
	}
	<-clientDone
}

// TestHandle_WriteResponseFails exercises handle's final "write response
// failed" branch: a genuine handshake and request decrypt/handle complete,
// then the server's response write fails.
func TestHandle_WriteResponseFails(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	js := newTestJoinServer(t, provider)

	clientConn, serverConn := net.Pipe()
	// Writes 1-2 are handshake message 2; write 3 (the response header) is
	// where this must fail.
	fc := &faultyConn{Conn: serverConn, failReadAfter: -1, failWriteAfter: 2}

	_, csrDER := generateTestCSR(t)
	reqJSON, err := json.Marshal(JoinRequest{Token: "tok", CSRDER: csrDER})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		rawJoinClient(clientConn, js, reqJSON, stopAfterResponse)
	}()

	done := make(chan struct{})
	go func() {
		js.handle(fc)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handle did not return after its response write was faulted")
	}
	<-clientDone
}

// TestJoinClientExchange_WriteMsg1Fails exercises the initiator exchange's
// "write handshake message 1" failure branch.
func TestJoinClientExchange_WriteMsg1Fails(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	js := newTestJoinServer(t, provider)

	clientConn, serverConn := net.Pipe()
	fc := &faultyConn{Conn: clientConn, failReadAfter: -1, failWriteAfter: 0}

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		js.handle(serverConn)
	}()

	_, csrDER := generateTestCSR(t)
	exchange, serverKey := testJoinClient(t, js)
	_, err := exchange(fc, serverKey, 5*time.Second, "tok", csrDER, "", 0, nil)
	fc.Close()
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("joinClientExchange with a faulted msg1 write = %v, want *FetchError", err)
	}
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("server handle did not return")
	}
}

// TestJoinClientExchange_ReadMsg2Fails exercises the initiator exchange's "read
// handshake message 2" failure branch: msg1 is really sent, then the client's
// read fails.
func TestJoinClientExchange_ReadMsg2Fails(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	js := newTestJoinServer(t, provider)

	clientConn, serverConn := net.Pipe()
	fc := &faultyConn{Conn: clientConn, failReadAfter: 0, failWriteAfter: -1}

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		js.handle(serverConn)
	}()

	_, csrDER := generateTestCSR(t)
	exchange, serverKey := testJoinClient(t, js)
	_, err := exchange(fc, serverKey, 5*time.Second, "tok", csrDER, "", 0, nil)
	fc.Close()
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("joinClientExchange with a faulted msg2 read = %v, want *FetchError", err)
	}
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("server handle did not return")
	}
}

// TestJoinClientExchange_WriteRequestFails exercises the initiator exchange's
// "write request ciphertext" failure branch: a genuine handshake completes
// (the client's first two writes, message 1's header and payload, succeed),
// then the client's next write (the request frame) fails.
func TestJoinClientExchange_WriteRequestFails(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	js := newTestJoinServer(t, provider)

	clientConn, serverConn := net.Pipe()
	fc := &faultyConn{Conn: clientConn, failReadAfter: -1, failWriteAfter: 2}

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		js.handle(serverConn)
	}()

	_, csrDER := generateTestCSR(t)
	exchange, serverKey := testJoinClient(t, js)
	_, err := exchange(fc, serverKey, 5*time.Second, "tok", csrDER, "", 0, nil)
	fc.Close()
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("joinClientExchange with a faulted request write = %v, want *FetchError", err)
	}
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("server handle did not return")
	}
}

// TestJoinClientExchange_ReadResponseFails exercises the initiator exchange's
// "read response ciphertext" failure branch: a genuine handshake and request
// write complete, then the client's read of the response fails.
func TestJoinClientExchange_ReadResponseFails(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	js := newTestJoinServer(t, provider)

	clientConn, serverConn := net.Pipe()
	fc := &faultyConn{Conn: clientConn, failReadAfter: 2, failWriteAfter: -1}

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		js.handle(serverConn)
	}()

	_, csrDER := generateTestCSR(t)
	exchange, serverKey := testJoinClient(t, js)
	_, err := exchange(fc, serverKey, 5*time.Second, "tok", csrDER, "", 0, nil)
	fc.Close()
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("joinClientExchange with a faulted response read = %v, want *FetchError", err)
	}
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("server handle did not return")
	}
}

// TestJoinClientExchange_HandshakeMessage2Invalid exercises the
// initiator exchange's handshake-failure branch, in the suite this runtime
// selects: the counterpart is not a real responder of either suite, so message
// 2 is garbage. The keys it pins belong to a server that is never started.
func TestJoinClientExchange_HandshakeMessage2Invalid(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer serverConn.Close()
		deadline := time.Now().Add(5 * time.Second)
		if _, err := noiseboot.ReadFrame(serverConn, deadline); err != nil {
			return
		}
		_ = noiseboot.WriteFrame(serverConn, []byte("not a real handshake message"), deadline)
	}()

	exchange, serverKey := testJoinClient(t, newTestJoinServer(t, nil))

	_, csrDER := generateTestCSR(t)
	_, err := exchange(clientConn, serverKey, 5*time.Second, "tok", csrDER, "", 0, nil)
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("joinClientExchange against a garbage msg2 = %v, want *FetchError", err)
	}
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("fake server did not return")
	}
}
