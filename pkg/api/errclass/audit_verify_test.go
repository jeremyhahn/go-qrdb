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

package errclass

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// TestClassify_AuditVerifyConditions pins the class of each condition that
// stops an audit chain verification from being performed. None is a tamper
// verdict; that is a field of the report, never an error.
func TestClassify_AuditVerifyConditions(t *testing.T) {
	cases := []struct {
		err  error
		want Class
	}{
		{&audit.VerificationUnsupportedError{}, Unimplemented},
		{&audit.ConcurrentRotationError{Path: "audit.ndjson", Attempts: 3}, Unavailable},
		{&audit.FileLockedError{Path: "audit.ndjson"}, Unavailable},
		{&audit.VerificationBusyError{}, Unavailable},
		// A cancelled or expired verification classifies through the context
		// error it unwraps to.
		{&audit.VerificationCanceledError{Path: "audit.ndjson", Err: context.Canceled}, Canceled},
		{&audit.VerificationCanceledError{Path: "audit.ndjson", Err: context.DeadlineExceeded}, DeadlineExceeded},
		// A file the walk could not read is a server fault.
		{&audit.FileError{Path: "audit.ndjson", Err: errors.New("permission denied")}, Unclassified},
	}
	for _, tc := range cases {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("Classify(%T %v) = %v, want %v", tc.err, tc.err, got, tc.want)
		}
	}
}
