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

package tenant

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestIsSystemTenantAgreesWithFromContextFallback proves the predicate's
// empty-string case matches FromContext's documented fallback: a context
// with no bound tenant resolves to tenantid.System, so IsSystemTenant must
// treat "" as system-scoped or the two would disagree on the same input.
func TestIsSystemTenantAgreesWithFromContextFallback(t *testing.T) {
	if !tenantid.IsSystem("") {
		t.Fatal("tenantid.IsSystem(\"\") = false, want true to agree with FromContext's empty-context fallback")
	}
}
