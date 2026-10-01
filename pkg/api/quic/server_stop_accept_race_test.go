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
	"sync"
	"testing"
	"time"
)

// TestServer_Stop_ClosesAConnectionAcceptedAfterItsClosePass pins the
// acceptLoop/Stop interleaving in which a connection is stored in s.conns
// only after Stop's close pass has ranged over it. Accept keeps returning
// connections that were queued when the listener closed, so this happens in
// production; the hooks only make it deterministic. Before the fix nothing
// closed that connection, its handleConnection sat in AcceptStream, and Stop
// hung in wg.Wait until the client's idle timeout.
func TestServer_Stop_ClosesAConnectionAcceptedAfterItsClosePass(t *testing.T) {
	accepted := make(chan struct{})
	closePassDone := make(chan struct{})
	var once sync.Once
	hooks := func(s *Server) {
		s.testHookAccepted = func() {
			once.Do(func() {
				close(accepted)
				<-closePassDone
			})
		}
		// Stop runs twice: here and in the test cleanup.
		s.testHookConnsClosed = sync.OnceFunc(func() { close(closePassDone) })
	}
	env := startIntegrationServerWithHandlers(t, &mockKVService{}, &mockAdminService{}, nil, hooks)

	conn := env.dialServer(t)
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never accepted the connection")
	}

	// dialServer's client idles out after 5s; Stop must not wait for that.
	limit := 2 * time.Second
	select {
	case <-stopAsync(env.srv):
	case <-time.After(limit):
		t.Fatalf("Stop did not return within %v: a connection accepted after its close pass was left open", limit)
	}
	select {
	case <-conn.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("the late connection was never closed")
	}
}
