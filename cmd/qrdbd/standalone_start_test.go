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
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// standaloneStartFlags is fullStartFlags in standalone mode: the raft-free
// data plane over the local engine the first database declares.
func standaloneStartFlags(dir string, p daemonPorts, backend string) *startFlags {
	f := fullStartFlags(dir, p)
	f.mode = capabilities.ModeStandalone
	if backend != "" {
		f.databases = []qrdbconfig.DatabaseConfig{{Name: "app", Backend: backend}}
	}
	return f
}

// TestRunStart_Standalone_ServesThenShutsDown boots the standalone data
// plane on each local backend to serving, then signals it down cleanly.
func TestRunStart_Standalone_ServesThenShutsDown(t *testing.T) {
	for _, backend := range []string{"", "memory"} {
		t.Run("backend="+backend, func(t *testing.T) {
			serving := servingHook(t)
			done := startDaemon(t, func(p daemonPorts) daemonLaunch {
				return runStartLaunch(t, standaloneStartFlags(t.TempDir(), p, backend), hookFired(serving))
			})
			if err := signalUntilReturn(t, done, syscall.SIGTERM); err != nil {
				t.Fatalf("runStart returned an error after a clean shutdown: %v", err)
			}
			resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
		})
	}
}

// TestRunStart_ServesWithOptionalWiring boots a raft node to serving with
// each optional piece of wiring an operator can turn on from a flag, then
// signals it down cleanly.
func TestRunStart_ServesWithOptionalWiring(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *startFlags)
	}{
		{
			name: "explicit tls client auth",
			setup: func(t *testing.T, f *startFlags) {
				f.tlsClientAuthSet = true
				f.tlsClientAuth = "request"
			},
		},
		{
			name: "backup directory",
			setup: func(t *testing.T, f *startFlags) {
				f.backupDir = t.TempDir()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serving := servingHook(t)
			done := startDaemon(t, func(p daemonPorts) daemonLaunch {
				f := fullStartFlags(t.TempDir(), p)
				tt.setup(t, f)
				return runStartLaunch(t, f, hookFired(serving))
			})
			if err := signalUntilReturn(t, done, syscall.SIGTERM); err != nil {
				t.Fatalf("runStart returned an error after a clean shutdown: %v", err)
			}
			resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
		})
	}
}

// TestRunStart_Standalone_RefusedBackends proves a standalone node refuses a
// raft backend and an unknown backend name, before it serves anything.
func TestRunStart_Standalone_RefusedBackends(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		check   func(t *testing.T, err error)
	}{
		{
			name:    "raft backend",
			backend: "raft",
			check: func(t *testing.T, err error) {
				var target *StandaloneRaftBackendError
				if !errors.As(err, &target) {
					t.Fatalf("runStart = %v (%T), want *StandaloneRaftBackendError", err, err)
				}
			},
		},
		{
			name:    "unsupported backend",
			backend: "tape",
			check: func(t *testing.T, err error) {
				var target *database.InvalidBackendError
				if !errors.As(err, &target) {
					t.Fatalf("runStart = %v (%T), want *database.InvalidBackendError", err, err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := drawDaemonPorts()
			if err != nil {
				t.Fatalf("draw ports: %v", err)
			}
			tt.check(t, runStartUntilExit(t, standaloneStartFlags(t.TempDir(), p, tt.backend)))
		})
	}
}

// runStartUntilExit reserves f's Noise port, runs runStart on f and returns
// what it returns, for a start that must fail before it serves. A daemon
// that serves instead is stopped and fails the test.
func runStartUntilExit(t *testing.T, f *startFlags) error {
	t.Helper()
	serving := servingHook(t)
	reserveNoisePort(t, f)
	done := make(chan error, 1)
	go func() { done <- runStart(f) }()
	guardDaemonShutdown(t, done)
	exited := waitReady(t, done, hookFired(serving), readyDeadline(t))
	if exited == nil {
		t.Fatal("runStart served, want a start that fails before serving")
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
	return exited.Err
}

// TestRunStart_FailsBeforeServing covers boot steps that end the start: an
// audit failure policy this build does not know, a backup directory that is
// a regular file, and an mTLS binding policy the user service cannot parse.
func TestRunStart_FailsBeforeServing(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *startFlags)
		check func(t *testing.T, err error)
	}{
		{
			name: "unknown audit failure policy",
			setup: func(t *testing.T, f *startFlags) {
				f.auditFailurePolicy = "shrug"
			},
			check: func(t *testing.T, err error) {
				var target *audit.InvalidFailurePolicyError
				if !errors.As(err, &target) || target.Value != "shrug" {
					t.Fatalf("runStart = %v (%T), want *audit.InvalidFailurePolicyError for %q", err, err, "shrug")
				}
			},
		},
		{
			name: "backup directory is a file",
			setup: func(t *testing.T, f *startFlags) {
				f.backupDir = filepath.Join(t.TempDir(), "backups")
				if err := os.WriteFile(f.backupDir, []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			},
			check: func(t *testing.T, err error) {
				var target *RestoreBackupError
				if !errors.As(err, &target) || target.Phase != "open-backup-store" {
					t.Fatalf("runStart = %v (%T), want *RestoreBackupError at open-backup-store", err, err)
				}
			},
		},
		{
			name: "invalid mtls binding policy",
			setup: func(t *testing.T, f *startFlags) {
				f.authEnabled = true
				f.mtlsBindingPolicy = "sometimes"
			},
			check: func(t *testing.T, err error) {
				var target *lifecycle.UserServiceError
				if !errors.As(err, &target) {
					t.Fatalf("runStart = %v (%T), want *lifecycle.UserServiceError", err, err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := drawDaemonPorts()
			if err != nil {
				t.Fatalf("draw ports: %v", err)
			}
			f := fullStartFlags(t.TempDir(), p)
			tt.setup(t, f)
			tt.check(t, runStartUntilExit(t, f))
		})
	}
}
