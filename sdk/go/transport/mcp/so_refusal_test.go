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

package mcp

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// TestSORefusals_RoundTripTyped: the refusal the server encodes (errclass)
// decodes on the MCP client as the typed errors sdk/go re-exports, the
// conflict's tenant included (go-qrdb#1129).
func TestSORefusals_RoundTripTyped(t *testing.T) {
	for _, sent := range []error{&auth.SORoleRequiredError{}, &auth.SORoleConflictError{TenantID: "t1"}} {
		reason, md, ok := errclass.Refusal(sent)
		if !ok {
			t.Fatalf("errclass.Refusal(%T) did not classify", sent)
		}
		got := mapToolError("qrdb_role_assign", &wire.ErrorPayload{
			Code: wire.CodePermissionDenied, Message: sent.Error(), Reason: reason, Metadata: md,
		})
		switch want := sent.(type) {
		case *auth.SORoleRequiredError:
			var e *auth.SORoleRequiredError
			if !errors.As(got, &e) {
				t.Fatalf("mapToolError() = %T %v, want *SORoleRequiredError", got, got)
			}
		case *auth.SORoleConflictError:
			var e *auth.SORoleConflictError
			if !errors.As(got, &e) || e.TenantID != want.TenantID {
				t.Fatalf("mapToolError() = %T %v, want *SORoleConflictError for %s", got, got, want.TenantID)
			}
		}
	}
}
