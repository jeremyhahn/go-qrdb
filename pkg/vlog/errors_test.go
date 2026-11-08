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

package vlog

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestTableWriteError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("disk full")
	e := &TableWriteError{Err: cause}
	if !strings.Contains(e.Error(), "disk full") {
		t.Fatalf("Error() = %q, want it to mention the cause", e.Error())
	}
	if e.Unwrap() != cause {
		t.Fatal("Unwrap() did not return the wrapped cause")
	}
}

func TestTableReadError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("corrupt page")
	e := &TableReadError{Err: cause}
	if !strings.Contains(e.Error(), "corrupt page") {
		t.Fatalf("Error() = %q, want it to mention the cause", e.Error())
	}
	if e.Unwrap() != cause {
		t.Fatal("Unwrap() did not return the wrapped cause")
	}
}

func TestCorruptTableError_Error(t *testing.T) {
	e := &CorruptTableError{Key: "\x00qrdb.audit/h/acme", Reason: "malformed head record"}
	msg := e.Error()
	if !strings.Contains(msg, "malformed head record") {
		t.Fatalf("Error() = %q, want it to mention the reason", msg)
	}
}

func TestInvalidTenantIDError_Error(t *testing.T) {
	e := &InvalidTenantIDError{TenantID: "acme/evil"}
	if !strings.Contains(e.Error(), "acme/evil") {
		t.Fatalf("Error() = %q, want it to mention the offending tenant id", e.Error())
	}
}

// TestNamedLogErrors_Error covers every named-log error's message carrying
// the detail a caller needs to act on it.
func TestNamedLogErrors_Error(t *testing.T) {
	id := LogID{TenantID: "acme", Database: "ledger", Log: "certs"}
	for name, tc := range map[string]struct {
		err  error
		want []string
	}{
		"invalid name":       {err: &InvalidLogNameError{Field: "log", Name: "a/b"}, want: []string{"log", `"a/b"`}},
		"invalid desc":       {err: &InvalidDescriptorError{Reason: "empty origin"}, want: []string{"empty origin"}},
		"exists":             {err: &LogExistsError{ID: id}, want: []string{"acme/ledger/certs", "exists"}},
		"not found":          {err: &LogNotFoundError{ID: id}, want: []string{"acme/ledger/certs", "not found"}},
		"append batch":       {err: &AppendBatchError{Count: 0}, want: []string{"0 entries", strconv.Itoa(MaxAppendPerCommand)}},
		"size out of range":  {err: &SizeOutOfRangeError{Size: 9, Limit: 8}, want: []string{"9", "8"}},
		"index out of range": {err: &IndexOutOfRangeError{Index: 5, Size: 5}, want: []string{"index 5", "size 5"}},
		"root mismatch":      {err: &CheckpointRootMismatchError{Size: 7}, want: []string{"root", "7"}},
		"checkpoint exists":  {err: &CheckpointExistsError{Size: 4}, want: []string{"4", "already exists"}},
		"empty checkpoint":   {err: &EmptyCheckpointError{Size: 3}, want: []string{"empty", "3"}},
		"expire not allowed": {err: &ExpireNotAllowedError{ID: id}, want: []string{"acme/ledger/certs", "expiring"}},
		"not audit log":      {err: &NotAuditLogError{ID: id}, want: []string{"acme/ledger/certs", "audit"}},
		"audit name taken":   {err: &AuditNameTakenError{ID: id}, want: []string{"acme/ledger/certs", "predates the audit instance", "delete it"}},
	} {
		t.Run(name, func(t *testing.T) {
			msg := tc.err.Error()
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Fatalf("Error() = %q, want it to mention %q", msg, w)
				}
			}
		})
	}
}
