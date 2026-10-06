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
	"crypto/tls"
	"io"
	"testing"
	"time"

	"golang.org/x/net/http2"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
)

// advertisedMaxConcurrentStreams opens a TLS HTTP/2 connection to a gRPC
// listener and returns the SETTINGS_MAX_CONCURRENT_STREAMS value from the
// server's first SETTINGS frame -- the cap a client actually sees on the wire.
func advertisedMaxConcurrentStreams(t *testing.T, addr string) uint32 {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h2"},
	})
	if err != nil {
		t.Fatalf("TLS dial %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatalf("write client preface: %v", err)
	}
	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(); err != nil {
		t.Fatalf("write SETTINGS: %v", err)
	}
	for {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		settings, ok := frame.(*http2.SettingsFrame)
		if !ok || settings.IsAck() {
			continue
		}
		v, ok := settings.Value(http2.SettingMaxConcurrentStreams)
		if !ok {
			t.Fatal("server SETTINGS frame carries no MAX_CONCURRENT_STREAMS")
		}
		return v
	}
}

// TestWithGRPCMaxConcurrentStreams_ReachesTheWire: the embedder's stream cap
// is what the gRPC listener advertises to every client in its HTTP/2
// SETTINGS, not merely a stored option -- and without the option the server
// advertises its own bounded default, never HTTP/2's unlimited one.
func TestWithGRPCMaxConcurrentStreams_ReachesTheWire(t *testing.T) {
	tests := []struct {
		name string
		opts []ServerOption
		want uint32
	}{
		{name: "default", want: grpcapi.DefaultMaxConcurrentStreams},
		{name: "embedder cap", opts: []ServerOption{WithGRPCMaxConcurrentStreams(7)}, want: 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := append([]ServerOption{WithCeremonyCA(testServerCA)}, tc.opts...)
			srv := New(testConfig(t, false, true), newMockKVService(), &mockAdminService{}, opts...)
			if err := srv.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer srv.Stop()

			if got := advertisedMaxConcurrentStreams(t, srv.GRPCAddr()); got != tc.want {
				t.Fatalf("advertised MAX_CONCURRENT_STREAMS = %d, want %d", got, tc.want)
			}
		})
	}
}
