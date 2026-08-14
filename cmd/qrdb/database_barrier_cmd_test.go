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

//go:build !integration

// database_barrier_cmd_test.go exercises the database seal, unseal,
// and barrier-status CLI subcommands via the existing executeDatabaseCmd helper.
package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// database seal
// ---------------------------------------------------------------------------

func TestDatabaseSealCmd_Success(t *testing.T) {
	var sealedName string
	dbSvc := &fnDatabaseService{
		createFn: nil,
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{Name: name}, nil
		},
		listFn: func() ([]database.DatabaseInfo, error) { return nil, nil },
		dropFn: func(_ string) error { return nil },
	}
	// Override SealDatabase via embedding a wrapper type that overrides.
	sealSvc := &sealableDatabaseService{
		fnDatabaseService: dbSvc,
		sealFn: func(_ context.Context, name string) error {
			sealedName = name
			return nil
		},
	}

	out, err := executeDatabaseCmd(t, sealSvc, "seal", "mydb")
	if err != nil {
		t.Fatalf("database seal: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output, got: %q", out)
	}
	if sealedName != "mydb" {
		t.Errorf("sealed name = %q, want mydb", sealedName)
	}
}

func TestDatabaseSealCmd_Error(t *testing.T) {
	dbSvc := &fnDatabaseService{
		createFn: nil,
		getFn:    func(name string) (*database.DatabaseInfo, error) { return nil, nil },
		listFn:   func() ([]database.DatabaseInfo, error) { return nil, nil },
		dropFn:   func(_ string) error { return nil },
	}
	sealSvc := &sealableDatabaseService{
		fnDatabaseService: dbSvc,
		sealFn: func(_ context.Context, _ string) error {
			return errors.New("barrier already sealed")
		},
	}

	_, err := executeDatabaseCmd(t, sealSvc, "seal", "mydb")
	if err == nil {
		t.Fatal("expected error from database seal")
	}
}

// ---------------------------------------------------------------------------
// database unseal
// ---------------------------------------------------------------------------

func TestDatabaseUnsealCmd_Success(t *testing.T) {
	var unsealedName, unsealedSecret string
	dbSvc := &fnDatabaseService{
		createFn: nil,
		getFn:    func(name string) (*database.DatabaseInfo, error) { return nil, nil },
		listFn:   func() ([]database.DatabaseInfo, error) { return nil, nil },
		dropFn:   func(_ string) error { return nil },
	}
	sealSvc := &sealableDatabaseService{
		fnDatabaseService: dbSvc,
		unsealFn: func(_ context.Context, name string, creds seal.Credentials) error {
			unsealedName = name
			unsealedSecret = string(creds.Secret)
			return nil
		},
	}

	out, err := executeDatabaseCmd(t, sealSvc, "unseal", "mydb", "--secret", "mysecret")
	if err != nil {
		t.Fatalf("database unseal: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output, got: %q", out)
	}
	if unsealedName != "mydb" {
		t.Errorf("unseal name = %q, want mydb", unsealedName)
	}
	if unsealedSecret != "mysecret" {
		t.Errorf("unseal secret = %q, want mysecret", unsealedSecret)
	}
}

func TestDatabaseUnsealCmd_Error(t *testing.T) {
	dbSvc := &fnDatabaseService{
		createFn: nil,
		getFn:    func(name string) (*database.DatabaseInfo, error) { return nil, nil },
		listFn:   func() ([]database.DatabaseInfo, error) { return nil, nil },
		dropFn:   func(_ string) error { return nil },
	}
	sealSvc := &sealableDatabaseService{
		fnDatabaseService: dbSvc,
		unsealFn: func(_ context.Context, _ string, _ seal.Credentials) error {
			return errors.New("wrong passphrase")
		},
	}

	_, err := executeDatabaseCmd(t, sealSvc, "unseal", "mydb", "--secret", "bad")
	if err == nil {
		t.Fatal("expected error from database unseal")
	}
}

// ---------------------------------------------------------------------------
// database barrier-status
// ---------------------------------------------------------------------------

func TestDatabaseBarrierStatusCmd_Sealed(t *testing.T) {
	dbSvc := &fnDatabaseService{
		createFn: nil,
		getFn:    func(name string) (*database.DatabaseInfo, error) { return nil, nil },
		listFn:   func() ([]database.DatabaseInfo, error) { return nil, nil },
		dropFn:   func(_ string) error { return nil },
	}
	sealSvc := &sealableDatabaseService{
		fnDatabaseService: dbSvc,
		barrierStatusFn: func(name string) (*seal.BarrierStatus, error) {
			return &seal.BarrierStatus{
				Sealed:         true,
				Strategy:       "software",
				HardwareBacked: false,
				InitializedAt:  time.Time{},
			}, nil
		},
	}

	out, err := executeDatabaseCmd(t, sealSvc, "barrier-status", "mydb")
	if err != nil {
		t.Fatalf("database barrier-status: %v", err)
	}
	if !strings.Contains(out, "true") {
		t.Errorf("expected 'true' (sealed) in output, got: %q", out)
	}
	if !strings.Contains(out, "software") {
		t.Errorf("expected 'software' strategy in output, got: %q", out)
	}
}

func TestDatabaseBarrierStatusCmd_Unsealed_WithInitTime(t *testing.T) {
	initTime := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	dbSvc := &fnDatabaseService{
		createFn: nil,
		getFn:    func(name string) (*database.DatabaseInfo, error) { return nil, nil },
		listFn:   func() ([]database.DatabaseInfo, error) { return nil, nil },
		dropFn:   func(_ string) error { return nil },
	}
	sealSvc := &sealableDatabaseService{
		fnDatabaseService: dbSvc,
		barrierStatusFn: func(name string) (*seal.BarrierStatus, error) {
			return &seal.BarrierStatus{
				Sealed:        false,
				Strategy:      "shamir",
				InitializedAt: initTime,
			}, nil
		},
	}

	out, err := executeDatabaseCmd(t, sealSvc, "barrier-status", "mydb")
	if err != nil {
		t.Fatalf("database barrier-status with init time: %v", err)
	}
	if !strings.Contains(out, "shamir") {
		t.Errorf("expected 'shamir' strategy in output, got: %q", out)
	}
	if !strings.Contains(out, "2026-01-15") {
		t.Errorf("expected init date in output, got: %q", out)
	}
}

func TestDatabaseBarrierStatusCmd_JSON(t *testing.T) {
	dbSvc := &fnDatabaseService{
		createFn: nil,
		getFn:    func(name string) (*database.DatabaseInfo, error) { return nil, nil },
		listFn:   func() ([]database.DatabaseInfo, error) { return nil, nil },
		dropFn:   func(_ string) error { return nil },
	}
	sealSvc := &sealableDatabaseService{
		fnDatabaseService: dbSvc,
		barrierStatusFn: func(name string) (*seal.BarrierStatus, error) {
			return &seal.BarrierStatus{Sealed: false, Strategy: "software"}, nil
		},
	}

	// Set the output format globally for this test.
	prevFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = prevFormat })

	out, err := executeDatabaseCmd(t, sealSvc, "barrier-status", "mydb")
	if err != nil {
		t.Fatalf("database barrier-status json: %v", err)
	}
	if !strings.Contains(out, `"strategy"`) {
		t.Errorf("expected strategy key in JSON, got: %q", out)
	}
}

func TestDatabaseBarrierStatusCmd_Error(t *testing.T) {
	dbSvc := &fnDatabaseService{
		createFn: nil,
		getFn:    func(name string) (*database.DatabaseInfo, error) { return nil, nil },
		listFn:   func() ([]database.DatabaseInfo, error) { return nil, nil },
		dropFn:   func(_ string) error { return nil },
	}
	sealSvc := &sealableDatabaseService{
		fnDatabaseService: dbSvc,
		barrierStatusFn: func(name string) (*seal.BarrierStatus, error) {
			return nil, errors.New("database not found")
		},
	}

	_, err := executeDatabaseCmd(t, sealSvc, "barrier-status", "notexist")
	if err == nil {
		t.Fatal("expected error from database barrier-status")
	}
}

// ---------------------------------------------------------------------------
// sealableDatabaseService wraps fnDatabaseService with overridable seal methods.
// ---------------------------------------------------------------------------

// sealableDatabaseService extends fnDatabaseService with configurable
// SealDatabase, UnsealDatabase, and DatabaseBarrierStatus implementations
// for testing the database barrier commands.
type sealableDatabaseService struct {
	*fnDatabaseService
	sealFn          func(ctx context.Context, name string) error
	unsealFn        func(ctx context.Context, name string, creds seal.Credentials) error
	barrierStatusFn func(name string) (*seal.BarrierStatus, error)
}

func (s *sealableDatabaseService) SealDatabase(ctx context.Context, name string) error {
	if s.sealFn != nil {
		return s.sealFn(ctx, name)
	}
	return nil
}

func (s *sealableDatabaseService) UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error {
	if s.unsealFn != nil {
		return s.unsealFn(ctx, name, creds)
	}
	return nil
}

func (s *sealableDatabaseService) DatabaseBarrierStatus(_ context.Context, name string) (*seal.BarrierStatus, error) {
	if s.barrierStatusFn != nil {
		return s.barrierStatusFn(name)
	}
	return &seal.BarrierStatus{}, nil
}
