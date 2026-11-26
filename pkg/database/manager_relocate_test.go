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

package database

import (
	"path/filepath"
	"testing"
)

func TestRelocatedPath(t *testing.T) {
	from := filepath.FromSlash("/pools/a/t1")
	to := filepath.FromSlash("/pools/b/t1")
	cases := []struct {
		dir  string
		want string
		ok   bool
	}{
		{dir: filepath.FromSlash("/pools/a/t1/databases/db"), want: filepath.FromSlash("/pools/b/t1/databases/db"), ok: true},
		{dir: from, want: to, ok: true},
		{dir: filepath.FromSlash("/pools/a/t10/databases/db")},
		{dir: filepath.FromSlash("/pools/a")},
		{dir: ""},
	}
	for _, c := range cases {
		got, ok := relocatedPath(c.dir, from, to)
		if ok != c.ok || got != c.want {
			t.Errorf("relocatedPath(%q) = %q, %v; want %q, %v", c.dir, got, ok, c.want, c.ok)
		}
	}
}
