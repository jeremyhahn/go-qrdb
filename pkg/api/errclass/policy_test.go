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
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestClassify_PolicyErrors pins the class of every ABAC policy service
// error, bare and wrapped.
func TestClassify_PolicyErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Class
	}{
		{"invalid", &service.PolicyInvalidError{Name: "p", Reason: "compile"}, InvalidArgument},
		{"not found", &service.PolicyNotFoundError{Tenant: "acme", Name: "p"}, NotFound},
		{"exists", &service.PolicyExistsError{Tenant: "acme", Name: "p"}, AlreadyExists},
		{"conflict", &service.PolicyConflictError{Tenant: "acme", Name: "p"}, Aborted},
		{"cross tenant", &service.PolicyCrossTenantError{CallerTenant: "acme", RequestedTenant: "other"}, PermissionDenied},
		{"corrupt", &service.PolicyCorruptError{Tenant: "acme", Key: "k", Err: errors.New("bad json")}, DataLoss},
		{"unsupported store", &service.PolicyStoreUnsupportedError{Tenant: "acme"}, Unimplemented},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Fatalf("Classify(%T) = %v, want %v", tc.err, got, tc.want)
			}
			if got := Classify(fmt.Errorf("wrapped: %w", tc.err)); got != tc.want {
				t.Fatalf("Classify(wrapped %T) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestClassify_PolicyCorruptWinsOverInvalid pins that a corrupt record whose
// cause is an invalid-name error is DataLoss, not InvalidArgument: it is
// persisted state, not a request the caller could fix.
func TestClassify_PolicyCorruptWinsOverInvalid(t *testing.T) {
	err := &service.PolicyCorruptError{Tenant: "acme", Key: "k", Err: &service.PolicyInvalidError{Name: "x", Reason: "record name does not match its key"}}
	if got := Classify(err); got != DataLoss {
		t.Fatalf("Classify = %v, want DataLoss", got)
	}
}
