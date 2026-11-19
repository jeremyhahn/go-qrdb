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

package lifecycle

import (
	"errors"
	"strings"
	"testing"
)

// TestWrappedErrors_MessageAndUnwrap proves each wrapping lifecycle error
// names its stage, carries its cause's message and unwraps to the cause.
func TestWrappedErrors_MessageAndUnwrap(t *testing.T) {
	cause := errors.New("underlying cause")
	cases := []struct {
		name  string
		err   error
		stage string
	}{
		{"barrier service", &BarrierServiceError{Err: cause}, "barrier service initialization failed"},
		{"database catalog", &DatabaseCatalogError{Err: cause}, "database catalog initialization failed"},
		{"rag chunk defaults", &RAGChunkDefaultsError{Err: cause}, "rag chunking defaults invalid"},
		{"node identity", &NodeIdentityError{Reason: "no certificate", Err: cause}, "node identity: no certificate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			if !strings.Contains(msg, tc.stage) || !strings.Contains(msg, cause.Error()) {
				t.Fatalf("Error() = %q; want it to name %q and the cause", msg, tc.stage)
			}
			if !errors.Is(tc.err, cause) {
				t.Fatalf("errors.Is(%T, cause) = false; want it to unwrap to the cause", tc.err)
			}
		})
	}
}

// TestNodeIdentityError_WithoutCause proves a node identity refusal with no
// underlying error reports only its reason and unwraps to nothing.
func TestNodeIdentityError_WithoutCause(t *testing.T) {
	err := &NodeIdentityError{Reason: "no certificate"}
	if got, want := err.Error(), "lifecycle: node identity: no certificate"; got != want {
		t.Fatalf("Error() = %q; want %q", got, want)
	}
	if err.Unwrap() != nil {
		t.Fatalf("Unwrap() = %v; want nil", err.Unwrap())
	}
}
