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

package publicpath

import "testing"

func TestIs_BuiltInAllowlist(t *testing.T) {
	for _, path := range []string{"/v1/ca/bootstrap", "/v1/config/initialized", "/v1/cluster/keydelivery", "/v1/cluster/nodekey"} {
		if !Is(path) {
			t.Errorf("Is(%q) = false, want true", path)
		}
	}
}

func TestIs_ExactMatchOnly(t *testing.T) {
	for _, path := range []string{"", "/", "/health", "/health/", "/health/wt", "/healthz", "/v1/cluster/", "/v1/cluster/other", "/v1/kv"} {
		if Is(path) {
			t.Errorf("Is(%q) = true, want false", path)
		}
	}
}
