//go:build qrdb_sql

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

package driver

import (
	"context"
	"errors"
	"testing"
)

// TestEmbeddedConnector_ConnectOnClosed verifies Connect on closed connector returns error.
func TestEmbeddedConnector_ConnectOnClosed(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeEmbedded,
		Backend:  "memory",
		Database: "closedtest",
	}
	c := NewEmbeddedConnector(cfg)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := c.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error for closed connector")
	}
	var cce *ConnectorClosedError
	if !errors.As(err, &cce) {
		t.Errorf("expected *ConnectorClosedError, got %T: %v", err, err)
	}
}

// TestEmbeddedConnector_CloseIsIdempotent verifies double Close does not error.
func TestEmbeddedConnector_CloseIsIdempotent(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeEmbedded,
		Backend:  "memory",
		Database: "idempotenttest",
	}
	c := NewEmbeddedConnector(cfg)

	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close (should be idempotent): %v", err)
	}
}

// TestEmbeddedConnector_MultipleConnectsShareEngine verifies same engine is reused.
func TestEmbeddedConnector_MultipleConnectsShareEngine(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeEmbedded,
		Backend:  "memory",
		Database: "sharedtest",
	}
	c := NewEmbeddedConnector(cfg)
	defer func() { _ = c.Close() }()

	conn1, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	defer func() { _ = conn1.Close() }()

	conn2, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	defer func() { _ = conn2.Close() }()

	// Both connections should use the same engine.
	ec1, ok1 := conn1.(*embeddedConn)
	ec2, ok2 := conn2.(*embeddedConn)
	if !ok1 || !ok2 {
		t.Fatal("expected *embeddedConn")
	}
	if ec1.engine != ec2.engine {
		t.Error("expected both connections to share the same GMS engine")
	}
}

// TestEmbeddedConnector_Driver verifies Driver() returns a Driver instance.
func TestEmbeddedConnector_Driver(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeEmbedded,
		Backend:  "memory",
		Database: "drivertest",
	}
	c := NewEmbeddedConnector(cfg)
	d := c.Driver()
	if d == nil {
		t.Fatal("expected non-nil driver")
	}
	if _, ok := d.(*Driver); !ok {
		t.Errorf("expected *Driver, got %T", d)
	}
}
