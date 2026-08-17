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
	"database/sql"
	sqld "database/sql/driver"
	"errors"
	"testing"
)

// TestDriver_Registration verifies the driver is registered under "qrdb".
func TestDriver_Registration(t *testing.T) {
	db, err := sql.Open("qrdb", "embedded://memory?database=regtest")
	if err != nil {
		t.Fatalf("sql.Open unexpected error: %v", err)
	}
	defer func() { _ = db.Close() }()
}

// TestDriver_OpenConnector_Embedded verifies OpenConnector returns EmbeddedConnector.
func TestDriver_OpenConnector_Embedded(t *testing.T) {
	d := &Driver{}
	connector, err := d.OpenConnector("embedded://memory?database=testdb")
	if err != nil {
		t.Fatalf("OpenConnector() unexpected error: %v", err)
	}

	ec, ok := connector.(*EmbeddedConnector)
	if !ok {
		t.Fatalf("expected *EmbeddedConnector, got %T", connector)
	}
	_ = ec
}

// TestDriver_OpenConnector_Network_REST verifies OpenConnector returns NetworkConnector for REST.
func TestDriver_OpenConnector_Network_REST(t *testing.T) {
	d := &Driver{}
	connector, err := d.OpenConnector("rest://localhost:63001/mydb")
	if err != nil {
		t.Fatalf("OpenConnector() unexpected error: %v", err)
	}

	nc, ok := connector.(*NetworkConnector)
	if !ok {
		t.Fatalf("expected *NetworkConnector, got %T", connector)
	}
	_ = nc
}

// TestDriver_OpenConnector_Network_GRPC verifies OpenConnector returns NetworkConnector for gRPC.
func TestDriver_OpenConnector_Network_GRPC(t *testing.T) {
	d := &Driver{}
	connector, err := d.OpenConnector("grpc://localhost:50051/mydb")
	if err != nil {
		t.Fatalf("OpenConnector() unexpected error: %v", err)
	}

	nc, ok := connector.(*NetworkConnector)
	if !ok {
		t.Fatalf("expected *NetworkConnector, got %T", connector)
	}
	_ = nc
}

// TestDriver_OpenConnector_InvalidDSN verifies OpenConnector returns error for bad DSN.
func TestDriver_OpenConnector_InvalidDSN(t *testing.T) {
	d := &Driver{}
	_, err := d.OpenConnector("")
	if err == nil {
		t.Fatal("expected error for empty DSN")
	}
	var parseErr *DSNParseError
	if !errors.As(err, &parseErr) {
		t.Errorf("expected *DSNParseError, got %T: %v", err, err)
	}
}

// TestDriver_OpenConnector_UnsupportedScheme verifies OpenConnector returns error for bad scheme.
func TestDriver_OpenConnector_UnsupportedScheme(t *testing.T) {
	d := &Driver{}
	_, err := d.OpenConnector("mysql://localhost/db")
	if err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
	var schemeErr *UnsupportedSchemeError
	if !errors.As(err, &schemeErr) {
		t.Errorf("expected *UnsupportedSchemeError, got %T: %v", err, err)
	}
}

// TestDriver_Open_Embedded verifies Open returns a valid embedded connection.
func TestDriver_Open_Embedded(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=opentest")
	if err != nil {
		t.Fatalf("Open() unexpected error: %v", err)
	}
	if conn == nil {
		t.Fatal("expected non-nil connection")
	}
	_ = conn.Close()
}

// TestDriver_Open_InvalidDSN verifies Open returns error for invalid DSN.
func TestDriver_Open_InvalidDSN(t *testing.T) {
	d := &Driver{}
	_, err := d.Open("bad://host/db")
	if err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}

// TestDriver_InterfaceCompliance verifies Driver implements required interfaces.
func TestDriver_InterfaceCompliance(t *testing.T) {
	var d sqld.Driver = &Driver{}
	if d == nil {
		t.Fatal("Driver should implement driver.Driver")
	}

	var dc sqld.DriverContext = &Driver{}
	if dc == nil {
		t.Fatal("Driver should implement driver.DriverContext")
	}
}
