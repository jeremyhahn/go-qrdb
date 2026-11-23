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

package moderncvfs

import (
	"database/sql"
	"strings"
	"testing"
)

func TestModerncDriver_OpensInMemory(t *testing.T) {
	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var v string
	if err := db.QueryRow("select sqlite_version()").Scan(&v); err != nil {
		t.Fatalf("sqlite_version: %v", err)
	}
	if !strings.HasPrefix(v, "3.") {
		t.Fatalf("sqlite_version = %q, want 3.x", v)
	}
}
