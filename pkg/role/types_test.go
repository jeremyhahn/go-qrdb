// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package role

import (
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

func TestRole_JSONEmptyPermissions(t *testing.T) {
	original := &Role{
		ID:          "test-uuid",
		Name:        "empty",
		TenantID:    "system",
		Permissions: []permission.Permission{},
		System:      false,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded Role
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Permissions == nil {
		t.Fatal("expected non-nil empty Permissions")
	}
	if len(decoded.Permissions) != 0 {
		t.Errorf("Permissions length: got %d, want 0", len(decoded.Permissions))
	}
}
