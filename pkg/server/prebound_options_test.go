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

package server

import (
	"net"
	"testing"
)

// TestPreboundSocketOptions pins that each pre-bound socket option records
// exactly the socket it was given, and that the options are independent.
func TestPreboundSocketOptions(t *testing.T) {
	restLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = restLn.Close() })
	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = grpcLn.Close() })
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	mcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = mcpLn.Close() })
	quicPC, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = quicPC.Close() })

	var o serverOptions
	WithRESTListener(restLn)(&o)
	if o.restListener != restLn || o.restPacketConn != nil || o.grpcListener != nil ||
		o.quicPacketConn != nil || o.mcpListener != nil {
		t.Fatalf("WithRESTListener set %+v", o)
	}
	WithRESTPacketConn(pc)(&o)
	WithGRPCListener(grpcLn)(&o)
	WithQUICPacketConn(quicPC)(&o)
	WithMCPListener(mcpLn)(&o)
	if o.restListener != restLn || o.restPacketConn != pc || o.grpcListener != grpcLn ||
		o.quicPacketConn != quicPC || o.mcpListener != mcpLn {
		t.Fatalf("options did not each record their own socket: %+v", o)
	}

	var zero serverOptions
	WithRESTListener(nil)(&zero)
	WithRESTPacketConn(nil)(&zero)
	WithGRPCListener(nil)(&zero)
	WithQUICPacketConn(nil)(&zero)
	WithMCPListener(nil)(&zero)
	if zero.restListener != nil || zero.restPacketConn != nil || zero.grpcListener != nil ||
		zero.quicPacketConn != nil || zero.mcpListener != nil {
		t.Fatal("a nil socket must leave the server binding its configured port")
	}
}
