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

import "testing"

func TestNamespaceOf(t *testing.T) {
	tests := []struct {
		key, want string
		ok        bool
	}{
		{"acme/app/k", "acme/app/", true},
		{"acme/app/", "acme/app/", true},
		{"system/system/x/y", "system/system/", true},
		{"__sys:cluster/features/nodes/1", "__sys:cluster/features/", true}, // looks namespaced: kept
		{"__sys:users/42", "", false},
		{"plain", "", false},
		{"acme/app", "", false},
		{"/app/k", "", false},
		{"acme//k", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := NamespaceOf([]byte(tt.key))
		if ok != tt.ok || string(got) != tt.want {
			t.Errorf("NamespaceOf(%q) = %q, %v; want %q, %v", tt.key, got, ok, tt.want, tt.ok)
		}
	}
}

// Every namespace ComposeNamespace builds -- whatever the tenant id and
// database -- is recognised, so no tenant database can fall outside it.
func TestNamespaceOf_RecognisesEveryComposedNamespace(t *testing.T) {
	for _, pair := range [][2]string{{"acme", "app"}, {"", ""}, {"t:x", "d"}, {"__sys:odd", "db"}, {"a.b", "c-d_e"}} {
		ns := ComposeNamespace(pair[0], pair[1])
		got, ok := NamespaceOf([]byte(ns + "any/key"))
		if !ok || string(got) != ns {
			t.Errorf("NamespaceOf(%q+key) = %q, %v; want %q", ns, got, ok, ns)
		}
	}
}
