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

package sdk

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// TestSDK_SeparationRefusalsAreReexported: an embedder (go-quicbaas) tells
// the compliance-mode refusals apart through sdk/go alone, and both answer
// 403 on every transport (go-qrdb#1129).
func TestSDK_SeparationRefusalsAreReexported(t *testing.T) {
	cases := []struct {
		name string
		err  error
		is   func(error) bool
	}{
		{"SORoleRequiredError", fmt.Errorf("roles:create: %w", &SORoleRequiredError{}), func(err error) bool {
			var e *SORoleRequiredError
			return errors.As(err, &e)
		}},
		{"SORoleConflictError", fmt.Errorf("roles assign: %w", &SORoleConflictError{TenantID: "t1"}), func(err error) bool {
			var e *SORoleConflictError
			return errors.As(err, &e) && e.TenantID == "t1"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.is(tc.err) {
				t.Fatalf("errors.As through the sdk alias did not match %v", tc.err)
			}
			if got := authmw.HTTPStatus(tc.err, http.StatusTeapot); got != http.StatusForbidden {
				t.Fatalf("HTTPStatus = %d, want 403", got)
			}
		})
	}
}
