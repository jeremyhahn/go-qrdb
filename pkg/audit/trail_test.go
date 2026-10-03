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

package audit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// OpenTrail composes the file, the ring and the operational log: a record
// is durable in the file and answerable from the trail.
func TestOpenTrail_RecordsDurablyAndAnswersQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultTrailFile)
	trail, err := OpenTrail(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := trail.Emit(ctx, AuditEvent{Actor: "a", Resource: "r", Action: "x", Outcome: OutcomeSuccess}); err != nil {
		t.Fatal(err)
	}
	page, err := trail.Query(ctx, Filter{Action: "x"})
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("query = %+v, %v", page, err)
	}
	if err := trail.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("the trail file holds nothing: %v", err)
	}
}

// A path that cannot be opened opens nothing.
func TestOpenTrail_UnopenablePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, DefaultTrailFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if trail, err := OpenTrail(filepath.Join(dir, DefaultTrailFile)); err == nil || trail != nil {
		t.Fatalf("OpenTrail over a directory = %v, %v", trail, err)
	}
}
