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

package feature

import (
	"errors"
	"strings"
	"testing"
)

func TestNotActiveError_ClusterWideMessage(t *testing.T) {
	msg := (&NotActiveError{Feature: "cluster-shard-ids"}).Error()
	want := `featuregate: cluster feature "cluster-shard-ids" is not active yet: every node must run a release that supports it`
	if msg != want {
		t.Fatalf("cluster-wide message = %q, want %q", msg, want)
	}
	if strings.Contains(msg, "shard 0") {
		t.Fatalf("cluster-wide message names shard 0: %q", msg)
	}
}

func TestNotActiveError_PerGroupMessage(t *testing.T) {
	msg := (&NotActiveError{Feature: "prefix-compaction", ShardID: 7}).Error()
	want := `featuregate: cluster feature "prefix-compaction" is not active yet on shard 7: every node must run a release that supports it`
	if msg != want {
		t.Fatalf("per-group message = %q, want %q", msg, want)
	}
}

func TestNotActiveError_QuotesTheFeatureName(t *testing.T) {
	msg := (&NotActiveError{Feature: `odd "name"`}).Error()
	if !strings.Contains(msg, `"odd \"name\""`) {
		t.Fatalf("message does not quote the feature name: %q", msg)
	}
}

func TestNotActiveError_IsComparesIdentity(t *testing.T) {
	a := &NotActiveError{Feature: "tenant-undo"}
	b := &NotActiveError{Feature: "tenant-undo"}
	if errors.Is(a, b) {
		t.Fatal("two distinct refusals compare equal under errors.Is; the type carries no Is method")
	}
	var other *NotActiveError
	if errors.As(errors.New("unrelated"), &other) {
		t.Fatal("errors.As matched an unrelated error")
	}
}
