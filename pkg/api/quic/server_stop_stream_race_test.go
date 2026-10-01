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

package quic

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
)

// TestServer_Stop_WaitsForInFlightHandlerBeforeClosingItsConnection is the
// go-qrdb#550 regression test for the QUIC response-vs-teardown race: before
// Stop waited on streamWG -- in-flight handlers, each now including its
// wait for the client's wire.ProtocolV2 acknowledgement (awaitStreamAck) --
// it actively closed every live connection BEFORE waiting for in-flight
// handlers to return, so a
// handler still computing its response -- exactly the shape a system-scope
// encryption migration's Quiesce phase produces when it recycles this very
// server from within the enable-encryption handler's own migration goroutine
// -- had its connection severed out from under it. The caller then saw
// "server shutting down" instead of the answer the handler had already
// decided to send.
//
// This test reproduces that shape deterministically, without depending on
// real migration timing: a handler blocks until the test releases it, Stop is
// invoked while it is still blocked, and the handler is released only after
// Stop has observed the connection as still live. The client must receive
// the handler's real response, not a connection error, and Stop must still
// return once the handler finishes.
func TestServer_Stop_WaitsForInFlightHandlerBeforeClosingItsConnection(t *testing.T) {
	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	const slowMethod = "test.slow"
	const wantPayload = "the real response, not a shutdown error"
	env := startIntegrationServerWithHandlers(t, &mockKVService{}, &mockAdminService{}, map[string]methodHandler{
		slowMethod: func(_ context.Context, _ []byte) ([]byte, error) {
			close(handlerEntered)
			<-releaseHandler
			return []byte(wantPayload), nil
		},
	})

	// The client acknowledges the response by closing its side only after
	// reading it (the deferred Close below), which is how the server knows
	// the reply was delivered before it closes the connection (see
	// awaitStreamAck).
	conn := env.dialServer(t)

	type result struct {
		status  uint16
		payload []byte
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		stream, err := conn.OpenStreamSync(ctx)
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		defer func() { _ = stream.Close() }()

		if err := wire.WriteRequest(stream, slowMethod, "", 1, nil); err != nil {
			resultCh <- result{err: err}
			return
		}
		_, status, payload, err := wire.ReadResponse(stream)
		resultCh <- result{status: status, payload: payload, err: err}
	}()

	select {
	case <-handlerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never entered")
	}

	stopDone := make(chan struct{})
	go func() {
		env.srv.Stop()
		close(stopDone)
	}()

	// Stop must not have finished yet: it should be waiting on the still-
	// in-flight handler rather than having already torn down the connection
	// carrying it.
	select {
	case <-stopDone:
		t.Fatal("Stop returned while the handler was still in flight")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseHandler)

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("client round trip failed: %v", res.err)
		}
		if res.status != wire.StatusOK {
			t.Fatalf("status = %d, want %d (StatusOK)", res.status, wire.StatusOK)
		}
		if string(res.payload) != wantPayload {
			t.Fatalf("payload = %q, want %q", res.payload, wantPayload)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("client never received a response")
	}

	select {
	case <-stopDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop never returned after the handler finished")
	}
}

// awaitAcceptClosing polls (never sleeps blindly) until srv.acceptClosing is
// true, so the test can deterministically open a stream inside the exact
// window Stop's admission-closing race exists in, rather than guessing at
// timing with a fixed delay.
func awaitAcceptClosing(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		srv.acceptClosingMu.Lock()
		closing := srv.acceptClosing
		srv.acceptClosingMu.Unlock()
		if closing {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("acceptClosing was never set")
}

// TestServer_Stop_RefusesStreamsAcceptedAfterDrainBegins is the Codex-review
// regression test for the second go-qrdb#550 blocker in the first version of
// this fix: handleConnection called streamWG.Add(1) for a newly accepted
// stream with no coordination against Stop's streamWG.Wait, so a stream
// admitted in the window between Stop observing the counter and Stop's
// connection-close pass escaped BOTH -- Stop never waited for it, and it was
// then severed by the very next line, reproducing the "server shutting down"
// failure this whole fix exists to close, from a different angle. Worse, an
// Add landing concurrently with a zero-valued Wait violates
// sync.WaitGroup's own contract and can panic.
//
// This test holds one handler in flight (so Stop blocks in its streamWG wait
// rather than returning immediately), waits for Stop to flip acceptClosing,
// and only THEN opens a second stream on the SAME connection -- squarely
// inside the window the race lived in. That stream must be refused outright
// (a reset, not a hang and not a served response), and Stop must still
// return once the first handler completes.
func TestServer_Stop_RefusesStreamsAcceptedAfterDrainBegins(t *testing.T) {
	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseHandler) })
	const slowMethod = "test.slow2"
	env := startIntegrationServerWithHandlers(t, &mockKVService{}, &mockAdminService{}, map[string]methodHandler{
		slowMethod: func(_ context.Context, _ []byte) ([]byte, error) {
			close(handlerEntered)
			<-releaseHandler
			return []byte("first response"), nil
		},
	})
	// Unconditional, and registered AFTER the server's own cleanup so it runs
	// first: if an assertion below fails and returns early, the handler(s)
	// parked on releaseHandler must still be freed, or Stop -- still waiting
	// on them via streamWG -- hangs the whole test past its timeout instead
	// of reporting the actual failure.
	t.Cleanup(release)

	conn := env.dialServer(t)

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stream, err := conn.OpenStreamSync(ctx)
		if err != nil {
			return
		}
		defer func() { _ = stream.Close() }()
		if err := wire.WriteRequest(stream, slowMethod, "", 1, nil); err != nil {
			return
		}
		_, _, _, _ = wire.ReadResponse(stream)
	}()

	select {
	case <-handlerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first handler never entered")
	}

	stopDone := make(chan struct{})
	go func() {
		env.srv.Stop()
		close(stopDone)
	}()

	// Precisely the window the race lived in: Stop has closed admission but
	// is still waiting on the first (in-flight) handler.
	awaitAcceptClosing(t, env.srv)
	select {
	case <-stopDone:
		t.Fatal("Stop returned before the first handler finished")
	default:
	}

	secondStream, err := conn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open second stream: %v", err)
	}
	_ = wire.WriteRequest(secondStream, slowMethod, "", 2, nil)

	secondResultCh := make(chan error, 1)
	go func() {
		_, _, _, err := wire.ReadResponse(secondStream)
		secondResultCh <- err
	}()

	select {
	case err := <-secondResultCh:
		if err == nil {
			t.Fatal("second stream: expected a reset/refusal, got a served response")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second stream: refused-or-not was never resolved (handleConnection let it hang)")
	}

	release()

	select {
	case <-firstDone:
	case <-time.After(10 * time.Second):
		t.Fatal("first stream's round trip never completed")
	}

	select {
	case <-stopDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop never returned")
	}
}
