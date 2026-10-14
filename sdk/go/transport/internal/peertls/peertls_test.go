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

package peertls

import (
	"crypto/tls"
	"testing"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

func TestState_TLSPeer(t *testing.T) {
	p := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{Version: tls.VersionTLS13, ServerName: "db.example.com"}}}

	cs := State(p)
	if cs == nil {
		t.Fatal("State = nil for a TLS peer")
	}
	if cs.Version != tls.VersionTLS13 || cs.ServerName != "db.example.com" {
		t.Fatalf("State = %+v; want the peer's connection state", cs)
	}
}

// plaintextInfo is the AuthInfo of a connection that is not TLS.
type plaintextInfo struct{}

func (plaintextInfo) AuthType() string { return "insecure" }

func TestState_NotTLS(t *testing.T) {
	if cs := State(&peer.Peer{AuthInfo: plaintextInfo{}}); cs != nil {
		t.Fatalf("State = %+v for a plaintext peer; want nil", cs)
	}
	if cs := State(&peer.Peer{}); cs != nil {
		t.Fatalf("State = %+v for a peer with no auth info; want nil", cs)
	}
}

func TestState_NilPeer(t *testing.T) {
	if cs := State(nil); cs != nil {
		t.Fatalf("State(nil) = %+v; want nil", cs)
	}
}
