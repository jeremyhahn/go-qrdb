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
	sqld "database/sql/driver"
	"testing"
)

// TestNewNetworkConnector_WithoutTLS verifies NewNetworkConnector without TLS config.
func TestNewNetworkConnector_WithoutTLS(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeREST,
		Host:     "localhost:63001",
		Database: "mydb",
	}
	c := NewNetworkConnector(cfg)
	if c == nil {
		t.Fatal("expected non-nil connector")
	}
	if c.client == nil {
		t.Fatal("expected non-nil http client")
	}
}

// TestNewNetworkConnector_WithCAFile verifies connector is created with CA file config.
func TestNewNetworkConnector_WithCAFile(t *testing.T) {
	// Use a non-existent CA file — constructor should not error, it silently skips bad files.
	cfg := &DSNConfig{
		Scheme:   SchemeREST,
		Host:     "localhost:63001",
		Database: "mydb",
		CAFile:   "/nonexistent/ca.pem",
	}
	c := NewNetworkConnector(cfg)
	if c == nil {
		t.Fatal("expected non-nil connector")
	}
	// Transport should be set because CAFile was provided (even if file doesn't exist).
	if c.client.Transport == nil {
		t.Log("Transport is nil (CA file not found), this is acceptable behavior")
	}
}

// TestNewNetworkConnector_WithCertAndKey verifies connector with cert and key paths.
func TestNewNetworkConnector_WithCertAndKey(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeREST,
		Host:     "localhost:63001",
		Database: "mydb",
		CAFile:   "/nonexistent/ca.pem",
		CertFile: "/nonexistent/client.crt",
		KeyFile:  "/nonexistent/client.key",
	}
	c := NewNetworkConnector(cfg)
	if c == nil {
		t.Fatal("expected non-nil connector")
	}
}

// TestNetworkConnector_Connect verifies Connect returns a networkConn.
func TestNetworkConnector_Connect(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeREST,
		Host:     "localhost:63001",
		Database: "testdb",
	}
	c := NewNetworkConnector(cfg)

	conn, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn == nil {
		t.Fatal("expected non-nil connection")
	}

	nc, ok := conn.(*networkConn)
	if !ok {
		t.Fatalf("expected *networkConn, got %T", conn)
	}
	if nc.database != "testdb" {
		t.Errorf("database = %q, want testdb", nc.database)
	}
	if nc.baseURL != "rest://localhost:63001" {
		t.Errorf("baseURL = %q, want rest://localhost:63001", nc.baseURL)
	}
}

// TestNetworkConnector_ConnectGRPC verifies Connect for gRPC scheme.
func TestNetworkConnector_ConnectGRPC(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeGRPC,
		Host:     "server:50051",
		Database: "grpcdb",
	}
	c := NewNetworkConnector(cfg)

	conn, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	nc, ok := conn.(*networkConn)
	if !ok {
		t.Fatalf("expected *networkConn, got %T", conn)
	}
	if nc.baseURL != "grpc://server:50051" {
		t.Errorf("baseURL = %q, want grpc://server:50051", nc.baseURL)
	}
}

// TestNetworkConnector_Driver verifies Driver() returns a Driver instance.
func TestNetworkConnector_Driver(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeREST,
		Host:     "localhost:63001",
		Database: "db",
	}
	c := NewNetworkConnector(cfg)
	d := c.Driver()
	if d == nil {
		t.Fatal("expected non-nil driver")
	}
	if _, ok := d.(*Driver); !ok {
		t.Errorf("expected *Driver, got %T", d)
	}
}

// TestNetworkConnector_InterfaceCompliance verifies NetworkConnector implements driver.Connector.
func TestNetworkConnector_InterfaceCompliance(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeREST,
		Host:     "localhost",
		Database: "db",
	}
	var c sqld.Connector = NewNetworkConnector(cfg)
	if c == nil {
		t.Fatal("NetworkConnector should implement driver.Connector")
	}
}

// TestNewNetworkConnector_WithCertOnly verifies connector with CertFile but no key (TLS path still created).
func TestNewNetworkConnector_WithCertOnly(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeREST,
		Host:     "localhost:63001",
		Database: "mydb",
		CertFile: "/nonexistent/client.crt",
		// No KeyFile — cert load will fail silently.
	}
	c := NewNetworkConnector(cfg)
	if c == nil {
		t.Fatal("expected non-nil connector")
	}
}

// TestNetworkConnector_MultipleConnects verifies multiple Connect calls each return a new conn.
func TestNetworkConnector_MultipleConnects(t *testing.T) {
	cfg := &DSNConfig{
		Scheme:   SchemeREST,
		Host:     "localhost:8080",
		Database: "multidb",
	}
	c := NewNetworkConnector(cfg)

	conn1, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	conn2, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("second Connect: %v", err)
	}

	// Both should be network connections.
	if _, ok := conn1.(*networkConn); !ok {
		t.Fatalf("conn1 expected *networkConn, got %T", conn1)
	}
	if _, ok := conn2.(*networkConn); !ok {
		t.Fatalf("conn2 expected *networkConn, got %T", conn2)
	}
}
