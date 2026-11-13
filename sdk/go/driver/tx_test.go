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
	sqld "database/sql/driver"
	"errors"
	"testing"
)

// TestEmbeddedTx_DoubleCommit verifies double commit returns TxDoneError.
func TestEmbeddedTx_DoubleCommit(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=dblcommittest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	cb, ok := conn.(sqld.ConnBeginTx)
	if !ok {
		t.Skip("conn does not implement ConnBeginTx")
	}

	tx, err := cb.BeginTx(context.Background(), sqld.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("first Commit: %v", err)
	}

	err = tx.Commit()
	if err == nil {
		t.Fatal("expected error for double commit")
	}
	var tde *TxDoneError
	if !errors.As(err, &tde) {
		t.Errorf("expected *TxDoneError, got %T: %v", err, err)
	}
}

// TestEmbeddedTx_DoubleRollback verifies double rollback returns TxDoneError.
func TestEmbeddedTx_DoubleRollback(t *testing.T) {
	d := &Driver{}
	conn, err := d.Open("embedded://memory?database=dblrolltest")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	cb, ok := conn.(sqld.ConnBeginTx)
	if !ok {
		t.Skip("conn does not implement ConnBeginTx")
	}

	tx, err := cb.BeginTx(context.Background(), sqld.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("first Rollback: %v", err)
	}

	err = tx.Rollback()
	if err == nil {
		t.Fatal("expected error for double rollback")
	}
	var tde *TxDoneError
	if !errors.As(err, &tde) {
		t.Errorf("expected *TxDoneError, got %T: %v", err, err)
	}
}

// TestNetworkTx_DoubleCommit verifies double commit on networkTx returns TxDoneError.
func TestNetworkTx_DoubleCommit(t *testing.T) {
	tx := &networkTx{}
	if err := tx.Commit(); err != nil {
		t.Fatalf("first Commit: %v", err)
	}
	err := tx.Commit()
	if err == nil {
		t.Fatal("expected error for double commit")
	}
	var tde *TxDoneError
	if !errors.As(err, &tde) {
		t.Errorf("expected *TxDoneError, got %T: %v", err, err)
	}
}

// TestNetworkTx_DoubleRollback verifies double rollback on networkTx returns TxDoneError.
func TestNetworkTx_DoubleRollback(t *testing.T) {
	tx := &networkTx{}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("first Rollback: %v", err)
	}
	err := tx.Rollback()
	if err == nil {
		t.Fatal("expected error for double rollback")
	}
	var tde *TxDoneError
	if !errors.As(err, &tde) {
		t.Errorf("expected *TxDoneError, got %T: %v", err, err)
	}
}

// TestNetworkTx_CommitThenRollback verifies commit prevents subsequent rollback.
func TestNetworkTx_CommitThenRollback(t *testing.T) {
	tx := &networkTx{}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	err := tx.Rollback()
	if err == nil {
		t.Fatal("expected error for rollback after commit")
	}
	var tde *TxDoneError
	if !errors.As(err, &tde) {
		t.Errorf("expected *TxDoneError, got %T: %v", err, err)
	}
}
