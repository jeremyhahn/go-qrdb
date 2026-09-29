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

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ===========================================================================
// AdminBinding — service error paths (81.8% -> target 100%)
// ===========================================================================

// TestAdminBinding_Health_ServiceError exercises the branch where
// cl.Admin().Health() returns an error after a successful connection.
func TestAdminBinding_Health_ServiceError(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	_, err := b.Health()
	if err == nil {
		t.Fatal("expected error when admin service health fails")
	}
}

// TestAdminBinding_Health_ServiceError_ReturnsNilResponse verifies nil
// response is returned alongside the error.
func TestAdminBinding_Health_ServiceError_ReturnsNilResponse(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	resp, err := b.Health()
	if err == nil {
		t.Fatal("expected error")
	}
	if resp != nil {
		t.Fatal("expected nil response on service error")
	}
}

// TestAdminBinding_Status_ServiceError exercises the branch where
// cl.Admin().Status() returns an error.
func TestAdminBinding_Status_ServiceError(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	_, err := b.Status()
	if err == nil {
		t.Fatal("expected error when admin service status fails")
	}
}

// TestAdminBinding_Status_ServiceError_ReturnsNilResponse verifies nil
// response is returned alongside the error.
func TestAdminBinding_Status_ServiceError_ReturnsNilResponse(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	resp, err := b.Status()
	if err == nil {
		t.Fatal("expected error")
	}
	if resp != nil {
		t.Fatal("expected nil response on service error")
	}
}

// TestAdminBinding_Peers_ServiceError exercises the branch where
// cl.Admin().Peers() returns an error.
func TestAdminBinding_Peers_ServiceError(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	_, err := b.Peers()
	if err == nil {
		t.Fatal("expected error when admin service peers fails")
	}
}

// TestAdminBinding_Peers_ServiceError_ReturnsNilResponse verifies nil
// response is returned alongside the error.
func TestAdminBinding_Peers_ServiceError_ReturnsNilResponse(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	resp, err := b.Peers()
	if err == nil {
		t.Fatal("expected error")
	}
	if resp != nil {
		t.Fatal("expected nil response on service error")
	}
}

// TestAdminBinding_Capabilities_ServiceError exercises the branch where
// cl.Admin().Capabilities() returns an error.
func TestAdminBinding_Capabilities_ServiceError(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	_, err := b.Capabilities()
	if err == nil {
		t.Fatal("expected error when admin service capabilities fails")
	}
}

// TestAdminBinding_Capabilities_ServiceError_ReturnsNilResponse verifies nil
// response is returned alongside the error.
func TestAdminBinding_Capabilities_ServiceError_ReturnsNilResponse(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	resp, err := b.Capabilities()
	if err == nil {
		t.Fatal("expected error")
	}
	if resp != nil {
		t.Fatal("expected nil response on service error")
	}
}

// TestAdminBinding_AddMember_ServiceError exercises the service error path
// in AdminBinding.AddMember.
func TestAdminBinding_AddMember_ServiceError(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	err := b.AddMember(1, 2, "127.0.0.1:8080")
	if err == nil {
		t.Fatal("expected error when admin service AddMember fails")
	}
}

// TestAdminBinding_RemoveMember_ServiceError exercises the service error path
// in AdminBinding.RemoveMember.
func TestAdminBinding_RemoveMember_ServiceError(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	err := b.RemoveMember(1, 2)
	if err == nil {
		t.Fatal("expected error when admin service RemoveMember fails")
	}
}

// TestAdminBinding_RequestSnapshot_ServiceError exercises the service error
// path in AdminBinding.RequestSnapshot.
func TestAdminBinding_RequestSnapshot_ServiceError(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	err := b.RequestSnapshot(1)
	if err == nil {
		t.Fatal("expected error when admin service RequestSnapshot fails")
	}
}

// TestAdminBinding_Drain_ServiceError exercises the service error path
// in AdminBinding.Drain.
func TestAdminBinding_Drain_ServiceError(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	err := b.Drain()
	if err == nil {
		t.Fatal("expected error when admin service Drain fails")
	}
}

// ===========================================================================
// barrier.go — service error paths for the canonical surface
// ===========================================================================

// TestBarrierInitCmd_ServiceError exercises the service error path of
// the canonical `barrier init` verb.
func TestBarrierInitCmd_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		initializeFn: func(_ context.Context, _ []byte) error {
			return errors.New("init: barrier already initialized")
		},
	}
	root := newBarrierTestRoot(t, svc)
	_, err := executeCommand(root, "barrier", "init",
		"--barrier-strategy", "passphrase", "--passphrase", "pass")
	if err == nil {
		t.Fatal("expected service error from barrier init")
	}
	if !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("expected service error message, got: %q", err.Error())
	}
}

// TestBarrierUnsealCmd_ServiceError exercises the error path of
// newBarrierUnsealCmd.
func TestBarrierUnsealCmd_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		unsealFn: func(_ context.Context, _ []byte) error {
			return errors.New("unseal: invalid secret")
		},
	}
	root := newBarrierTestRoot(t, svc)
	_, err := executeCommand(root, "barrier", "unseal", "--passphrase", "wrongpass")
	if err == nil {
		t.Fatal("expected error from barrier unseal when service fails")
	}
	if !strings.Contains(err.Error(), "invalid secret") {
		t.Fatalf("expected error message in output, got: %q", err.Error())
	}
}

// ===========================================================================
// barrier.go — YAML output paths for newBarrierStatusCmd (83.3%)
// ===========================================================================

// TestBarrierStatusCmd_YAMLOutput exercises the YAML branch of
// newBarrierStatusCmd.
func TestBarrierStatusCmd_YAMLOutput(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "--output", "yaml", "barrier", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ===========================================================================
// cleanupPIDFile — error path when resolveConfigDir fails (57.1%)
// ===========================================================================

// TestCleanupPIDFile_ConfigDirError exercises the error path in cleanupPIDFile
// by making os.UserConfigDir() return an error (unset HOME and XDG_CONFIG_HOME).
func TestCleanupPIDFile_ConfigDirError(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	log := testLogger(t)
	d := NewDaemonBinding(log, "")
	// configDir is empty — resolveConfigDir will call os.UserConfigDir() which
	// fails when both HOME and XDG_CONFIG_HOME are unset on Linux.
	d.cleanupPIDFile() // must not panic
}

// TestCleanupPIDFile_MkdirAllError exercises the error path where configDir is
// set but MkdirAll fails.
func TestCleanupPIDFile_MkdirAllError(t *testing.T) {
	blockFile := filepath.Join(t.TempDir(), "block")
	if err := os.WriteFile(blockFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	log := testLogger(t)
	d := NewDaemonBinding(log, "")
	d.configDir = blockFile + "/sub" // sub-path of a regular file fails MkdirAll

	d.cleanupPIDFile() // must not panic
}

// ===========================================================================
// resolveConfigDir — MkdirAll error when configDir is under a regular file
// ===========================================================================

// TestResolveConfigDir_MkdirAllError exercises the error branch when
// configDir itself cannot be created.
func TestResolveConfigDir_MkdirAllError(t *testing.T) {
	blockFile := filepath.Join(t.TempDir(), "block")
	if err := os.WriteFile(blockFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	log := testLogger(t)
	d := NewDaemonBinding(log, "")
	d.configDir = blockFile + "/sub" // attempting to create sub-path of a file

	_, err := d.resolveConfigDir()
	if err == nil {
		t.Fatal("expected error when configDir path cannot be created")
	}
}

// TestResolveConfigDir_MkdirAllError_NonNil verifies the error is non-nil.
func TestResolveConfigDir_MkdirAllError_NonNil(t *testing.T) {
	blockFile := filepath.Join(t.TempDir(), "block")
	if err := os.WriteFile(blockFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	log := testLogger(t)
	d := NewDaemonBinding(log, "")
	d.configDir = blockFile + "/sub"

	result, err := d.resolveConfigDir()
	if err == nil {
		t.Fatal("expected error")
	}
	if result != "" {
		t.Fatalf("expected empty result on error, got: %q", result)
	}
}

// ===========================================================================
// barrier.go — service error paths for ops verbs (not part of canonical
// surface but retained for ops parity)
// ===========================================================================

// TestBarrierRecoveryDeleteCmd_ServiceError exercises the error return
// in newBarrierRecoveryDeleteCmd.
func TestBarrierRecoveryDeleteCmd_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		deleteRecoveryFn: func(_ context.Context) error {
			return errors.New("delete-recovery: operation not allowed")
		},
	}
	root := newBarrierTestRoot(t, svc)
	_, err := executeCommand(root, "barrier", "recovery", "delete")
	if err == nil {
		t.Fatal("expected error from recovery delete when service fails")
	}
}
