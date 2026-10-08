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

// Fault-injection tests for handle (the Noise_NK join listener's responder
// side) and joinClientExchange (its initiator side). Both take a net.Conn
// precisely so a test can fail a specific Read or Write deterministically --
// see their own doc comments -- rather than racing real socket timing to
// reach a mid-exchange peer-close or frame-failure branch. This file is that
// test: a faultyConn wraps one real net.Pipe() end and fails Read or Write
// from a configured call number onward, while the other end runs the real
// counterpart (a genuine Noise_NK session), so the failure lands exactly on
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
// valid, so none of them need a token actually minted into it.
func newTestJoinServer(t *testing.T, provider cakey.Provider) *JoinServer {
	t.Helper()
	skipIfNoiseUnavailable(t)
	key, err := noiseproto.GenerateStaticKey()
	if err != nil {
		t.Fatalf("GenerateStaticKey: %v", err)
	}
	t.Cleanup(key.Destroy)
	return &JoinServer{
		key:         key,
		store:       kvstore.NewMemoryKVStore(),
		trustDomain: testTrustDomain,
		provider:    provider,
		logger:      discardLogger(),
	}
}

// clientSendMsg1 builds a fresh initiator session against serverPub, writes
// handshake message 1 to conn, and returns the session for the caller to
// continue (or abandon) the handshake. It never calls t.Fatalf: it runs in a
// goroutine in every caller, where FailNow is unsafe.
func clientSendMsg1(conn net.Conn, serverPub []byte, deadline time.Time) (*noiseproto.Session, error) {
	sess, err := noiseproto.NewSession(&noiseproto.SessionConfig{
		Pattern:       noise.HandshakeNK,
		PeerStaticKey: serverPub,
		IsInitiator:   true,
	})
	if err != nil {
		return nil, err
	}
	if err := sess.InitHandshake(); err != nil {
		return nil, err
	}
	msg1, _, err := sess.HandshakeMessage(nil)
	if err != nil {
		return nil, err
	}
	if err := noiseboot.WriteFrame(conn, msg1, deadline); err != nil {
		return nil, err
	}
	return sess, nil
}

// clientCompleteHandshake reads handshake message 2 from conn and completes
// sess. See clientSendMsg1 on why this returns rather than fails.
func clientCompleteHandshake(conn net.Conn, sess *noiseproto.Session, deadline time.Time) error {
	msg2, err := noiseboot.ReadFrame(conn, deadline)
	if err != nil {
		return err
	}
	_, done, err := sess.HandshakeMessage(msg2)
	if err != nil {
		return err
	}
	if !done {
		return ErrHandshakeIncomplete
	}
	return nil
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
		deadline := time.Now().Add(5 * time.Second)
		_, _ = clientSendMsg1(clientConn, js.PublicKey(), deadline)
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
		deadline := time.Now().Add(5 * time.Second)
		sess, err := clientSendMsg1(clientConn, js.PublicKey(), deadline)
		if err != nil {
			return
		}
		if err := clientCompleteHandshake(clientConn, sess, deadline); err != nil {
			return
		}
		ciphertext, err := sess.Encrypt([]byte(`{"token":"tok"}`))
		if err != nil {
			return
		}
		_ = noiseboot.WriteFrame(clientConn, ciphertext, deadline)
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
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		deadline := time.Now().Add(5 * time.Second)
		sess, err := clientSendMsg1(clientConn, js.PublicKey(), deadline)
		if err != nil {
			return
		}
		if err := clientCompleteHandshake(clientConn, sess, deadline); err != nil {
			return
		}
		reqJSON, err := json.Marshal(JoinRequest{Token: "tok", CSRDER: csrDER})
		if err != nil {
			return
		}
		ciphertext, err := sess.Encrypt(reqJSON)
		if err != nil {
			return
		}
		if err := noiseboot.WriteFrame(clientConn, ciphertext, deadline); err != nil {
			return
		}
		_, _ = noiseboot.ReadFrame(clientConn, deadline)
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

// TestJoinClientExchange_WriteMsg1Fails exercises joinClientExchange's
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
	_, err := joinClientExchange(fc, js.PublicKey(), 5*time.Second, "tok", csrDER, "", 0, nil)
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

// TestJoinClientExchange_ReadMsg2Fails exercises joinClientExchange's "read
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
	_, err := joinClientExchange(fc, js.PublicKey(), 5*time.Second, "tok", csrDER, "", 0, nil)
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

// TestJoinClientExchange_WriteRequestFails exercises joinClientExchange's
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
	_, err := joinClientExchange(fc, js.PublicKey(), 5*time.Second, "tok", csrDER, "", 0, nil)
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

// TestJoinClientExchange_ReadResponseFails exercises joinClientExchange's
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
	_, err := joinClientExchange(fc, js.PublicKey(), 5*time.Second, "tok", csrDER, "", 0, nil)
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

// TestJoinClientExchange_HandshakeMessage2Invalid exercises
// joinClientExchange's handshake-incomplete branch: the counterpart is not a
// real Noise_NK responder, so message 2 is garbage.
func TestJoinClientExchange_HandshakeMessage2Invalid(t *testing.T) {
	skipIfNoiseUnavailable(t)
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

	key, err := noiseproto.GenerateStaticKey()
	if err != nil {
		t.Fatalf("GenerateStaticKey: %v", err)
	}
	defer key.Destroy()

	_, csrDER := generateTestCSR(t)
	_, err = joinClientExchange(clientConn, key.PublicBytes(), 5*time.Second, "tok", csrDER, "", 0, nil)
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
