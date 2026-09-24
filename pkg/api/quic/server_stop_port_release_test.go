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
	"net"
	"testing"
)

// TestServer_Stop_ReleasesThePortBeforeReturning pins that a server can be
// started again on the same address the moment Stop returns, even when Stop
// had to close a live client connection. A system-scope enable-encryption
// migration does exactly that: it stops this server and starts its
// replacement on the same port. quic-go keeps a closed connection's IDs
// registered for about three probe timeouts so it can repeat
// CONNECTION_CLOSE to a peer that retransmits, and a listener created with
// quic.ListenAddr keeps its UDP socket open until they expire -- so the
// replacement's bind failed with "address already in use" and the migration
// left the node without a QUIC listener.
func TestServer_Stop_ReleasesThePortBeforeReturning(t *testing.T) {
	env := startAckTestServer(t)

	// A live connection that Stop has to close.
	conn := env.dialServer(t)
	_ = requestWithoutClosing(t, conn, ackTestMethod).Close()

	addr := env.srv.Addr()
	env.srv.Stop()

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("resolve %s: %v", addr, err)
	}
	rebound, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatalf("bind %s right after Stop: %v", addr, err)
	}
	_ = rebound.Close()
}
