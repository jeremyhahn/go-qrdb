//go:build !integration

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

package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// silentUDPAddr returns the address of a UDP socket that is held open for the
// test and never answers, so a QUIC handshake against it can only end by its
// bound.
func silentUDPAddr(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc.LocalAddr().String()
}

// TestBuildClient_QUICConnectBoundedByTimeout pins that --timeout bounds the
// QUIC connect: against a peer that never answers, buildClient gives up with
// the deadline once the caller's timeout is spent, not after a fixed budget.
func TestBuildClient_QUICConnectBoundedByTimeout(t *testing.T) {
	const timeout = 300 * time.Millisecond
	addr := silentUDPAddr(t)

	start := time.Now()
	client, err := buildClient(context.Background(), transportQUIC, addr, "", "", "", timeout)
	elapsed := time.Since(start)
	if client != nil {
		_ = client.Close()
	}

	var connErr *ConnectError
	if !errors.As(err, &connErr) {
		t.Fatalf("buildClient error = %T (%v), want *ConnectError", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("buildClient error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed < timeout || elapsed > timeout+5*time.Second {
		t.Errorf("buildClient returned after %v, want about the %v --timeout", elapsed, timeout)
	}
}

// TestBuildClient_QUICConnectHonoursCallerDeadline pins that a caller context
// deadline shorter than --timeout bounds the QUIC connect.
func TestBuildClient_QUICConnectHonoursCallerDeadline(t *testing.T) {
	addr := silentUDPAddr(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	client, err := buildClient(ctx, transportQUIC, addr, "", "", "", time.Hour)
	elapsed := time.Since(start)
	if client != nil {
		_ = client.Close()
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("buildClient error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("buildClient returned after %v, want about the caller's 200ms deadline", elapsed)
	}
}
