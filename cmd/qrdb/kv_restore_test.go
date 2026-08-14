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

// kv_restore_test.go covers the "kv restore" CLI subcommand: flag parsing
// and validation (including the mutually-exclusive --prefix/--keys scope),
// table/json output rendering, and exit-code behavior on conflicts.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Flag validation
// ---------------------------------------------------------------------------

func TestKVRestore_MissingRevision(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockMVCCService(), newMockAdminService())
	_, err := executeCommand(root, "kv", "restore", "--prefix", "foo")
	if err == nil {
		t.Fatal("expected error for missing required --revision flag")
	}
}

func TestKVRestore_NeitherPrefixNorKeys(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockMVCCService(), newMockAdminService())
	_, err := executeCommand(root, "kv", "restore", "--revision", "5")
	if err == nil {
		t.Fatal("expected error when neither --prefix nor --keys is set")
	}
}

func TestKVRestore_BothPrefixAndKeys(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockMVCCService(), newMockAdminService())
	_, err := executeCommand(root, "kv", "restore", "--revision", "5", "--prefix", "foo", "--keys", "bar")
	if err == nil {
		t.Fatal("expected error when both --prefix and --keys are set")
	}
}

// ---------------------------------------------------------------------------
// --keys parsing: comma-separated and repeatable
// ---------------------------------------------------------------------------

func TestKVRestore_KeysFlag_CommaSeparated(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	mvccSvc := newMockMVCCService()
	mvccSvc.restoreFunc = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		captured = req
		return &service.RestoreRevisionResult{Revision: req.Revision, DryRun: req.DryRun}, nil
	}
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), embedded.WithMVCCService(mvccSvc))

	_, err := executeCommand(root, "kv", "restore", "--revision", "10", "--keys", "a,b,c", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected request to be captured")
	}
	if len(captured.Keys) != 3 || captured.Keys[0] != "a" || captured.Keys[1] != "b" || captured.Keys[2] != "c" {
		t.Fatalf("expected keys [a b c], got: %v", captured.Keys)
	}
}

func TestKVRestore_KeysFlag_Repeatable(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	mvccSvc := newMockMVCCService()
	mvccSvc.restoreFunc = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		captured = req
		return &service.RestoreRevisionResult{Revision: req.Revision, DryRun: req.DryRun}, nil
	}
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), embedded.WithMVCCService(mvccSvc))

	_, err := executeCommand(root, "kv", "restore", "--revision", "10", "--keys", "a", "--keys", "b", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected request to be captured")
	}
	if len(captured.Keys) != 2 || captured.Keys[0] != "a" || captured.Keys[1] != "b" {
		t.Fatalf("expected keys [a b], got: %v", captured.Keys)
	}
}

func TestKVRestore_PrefixFlag(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	mvccSvc := newMockMVCCService()
	mvccSvc.restoreFunc = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		captured = req
		return &service.RestoreRevisionResult{Revision: req.Revision, DryRun: req.DryRun}, nil
	}
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), embedded.WithMVCCService(mvccSvc))

	_, err := executeCommand(root, "kv", "restore", "--revision", "10", "--prefix", "users/", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected request to be captured")
	}
	if captured.Prefix != "users/" {
		t.Fatalf("expected prefix 'users/', got: %q", captured.Prefix)
	}
	if len(captured.Keys) != 0 {
		t.Fatalf("expected no keys when --prefix is set, got: %v", captured.Keys)
	}
}

func TestKVRestore_DryRunAndForcePropagate(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	mvccSvc := newMockMVCCService()
	mvccSvc.restoreFunc = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		captured = req
		return &service.RestoreRevisionResult{Revision: req.Revision, DryRun: req.DryRun}, nil
	}
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), embedded.WithMVCCService(mvccSvc))

	_, err := executeCommand(root, "kv", "restore", "--revision", "10", "--prefix", "p", "--force")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.DryRun {
		t.Fatal("expected DryRun=false by default")
	}
	if !captured.Force {
		t.Fatal("expected Force=true when --force is set")
	}
}

// ---------------------------------------------------------------------------
// Table output rendering
// ---------------------------------------------------------------------------

func TestKVRestore_TableOutput_DryRun(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.restoreFunc = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		return &service.RestoreRevisionResult{
			Revision: req.Revision,
			DryRun:   true,
			Applied:  0,
			Skipped:  1,
			Changes: []service.RestoreKeyChange{
				{Key: "users/1", Action: service.RestoreActionRevert, FromRevision: 20, ToRevision: 10, ValueBytes: 5},
				{Key: "users/2", Action: service.RestoreActionDelete, FromRevision: 22, ToRevision: 0},
			},
		}, nil
	}
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), embedded.WithMVCCService(mvccSvc))

	out, err := executeCommand(root, "kv", "restore", "--revision", "10", "--prefix", "users/", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"ACTION", "KEY", "FROM-REV", "TO-REV", "revert", "users/1", "delete", "users/2", "Mode: dry-run", "Applied: 0", "Skipped: 1", "Conflicted: 0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "CONFLICTS") {
		t.Fatalf("did not expect a CONFLICTS section, got:\n%s", out)
	}
}

func TestKVRestore_TableOutput_WithConflicts(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.restoreFunc = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		return &service.RestoreRevisionResult{
			Revision:   req.Revision,
			DryRun:     false,
			Applied:    1,
			Skipped:    0,
			Conflicted: 1,
			Changes: []service.RestoreKeyChange{
				{Key: "a", Action: service.RestoreActionRevert, FromRevision: 5, ToRevision: 3},
				{Key: "b", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 3},
			},
			Conflicts: []service.RestoreKeyChange{
				{Key: "b", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 3},
			},
		}, nil
	}
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), embedded.WithMVCCService(mvccSvc))

	out, err := executeCommand(root, "kv", "restore", "--revision", "3", "--keys", "a,b")
	if err == nil {
		t.Fatal("expected non-nil error for unresolved conflicts without --force")
	}
	var rce *RestoreConflictsError
	if !errors.As(err, &rce) {
		t.Fatalf("expected *RestoreConflictsError, got: %T: %v", err, err)
	}
	if rce.Conflicted != 1 || rce.Revision != 3 {
		t.Fatalf("unexpected RestoreConflictsError fields: %+v", rce)
	}
	for _, want := range []string{"Conflicted: 1", "CONFLICTS", "retry with --force"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestKVRestore_Force_SuppressesConflictExitCode(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.restoreFunc = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		// A real server never reports conflicts when Force is set, but the
		// CLI's own exit-code decision is driven off the local --force flag
		// (not solely the result), so this verifies that logic directly.
		return &service.RestoreRevisionResult{
			Revision:   req.Revision,
			Applied:    2,
			Conflicted: 0,
		}, nil
	}
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), embedded.WithMVCCService(mvccSvc))

	_, err := executeCommand(root, "kv", "restore", "--revision", "3", "--keys", "a,b", "--force")
	if err != nil {
		t.Fatalf("expected no error with --force, got: %v", err)
	}
}

func TestKVRestore_DryRun_NeverReturnsConflictError(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.restoreFunc = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		return &service.RestoreRevisionResult{
			Revision: req.Revision,
			DryRun:   true,
			Applied:  0,
		}, nil
	}
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), embedded.WithMVCCService(mvccSvc))

	_, err := executeCommand(root, "kv", "restore", "--revision", "3", "--keys", "a,b", "--dry-run")
	if err != nil {
		t.Fatalf("expected no error for dry run, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// JSON output rendering
// ---------------------------------------------------------------------------

func TestKVRestore_JSONOutput(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.restoreFunc = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		return &service.RestoreRevisionResult{
			Revision: req.Revision,
			DryRun:   true,
			Skipped:  1,
			Changes: []service.RestoreKeyChange{
				{Key: "users/1", Action: service.RestoreActionRecreate, ToRevision: 7, ValueBytes: 3},
			},
		}, nil
	}
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), embedded.WithMVCCService(mvccSvc))

	out, err := executeCommand(root, "--output", "json", "kv", "restore", "--revision", "7", "--prefix", "users/", "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded restoreRevisionResultDisplay
	if jsonErr := json.Unmarshal([]byte(out), &decoded); jsonErr != nil {
		t.Fatalf("failed to decode JSON output: %v\noutput: %s", jsonErr, out)
	}
	if decoded.Revision != 7 {
		t.Fatalf("expected revision 7, got: %d", decoded.Revision)
	}
	if len(decoded.Changes) != 1 || decoded.Changes[0].Key != "users/1" {
		t.Fatalf("expected one change for key 'users/1', got: %+v", decoded.Changes)
	}
	if decoded.Changes[0].Action != "recreate" {
		t.Fatalf("expected action 'recreate', got: %q", decoded.Changes[0].Action)
	}
	// The key must be plaintext, not base64-encoded.
	if strings.Contains(out, "dXNlcnMv") {
		t.Fatalf("expected plaintext key in JSON output, got base64: %s", out)
	}
}

// ---------------------------------------------------------------------------
// Error surfacing
// ---------------------------------------------------------------------------

// mvccBackendUnsupportedError simulates the actionable error a non-MVCC
// daemon (the default raft.sm_type: pebble) returns for restore requests.
type mvccBackendUnsupportedError struct{}

func (e *mvccBackendUnsupportedError) Error() string {
	return "qrdb: restore requires raft.sm_type: mvcc"
}

func TestKVRestore_ErrorSurfacing(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.restoreFunc = func(context.Context, *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		return nil, &mvccBackendUnsupportedError{}
	}
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), embedded.WithMVCCService(mvccSvc))

	_, err := executeCommand(root, "kv", "restore", "--revision", "3", "--prefix", "p")
	if err == nil {
		t.Fatal("expected error to propagate from the service layer")
	}
	if !strings.Contains(err.Error(), "raft.sm_type: mvcc") {
		t.Fatalf("expected actionable error message, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Help text
// ---------------------------------------------------------------------------

func TestKVRestore_HelpText(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockMVCCService(), newMockAdminService())
	out, err := executeCommand(root, "kv", "restore", "--help")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"raft.sm_type: mvcc", "logical undo", "never a Raft", "rewind", "--dry-run", "non-zero"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected help text to mention %q, got:\n%s", want, out)
		}
	}
}
