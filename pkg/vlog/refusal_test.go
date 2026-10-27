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

package vlog

import (
	"reflect"
	"testing"
)

func TestRefusalMetadata_RoundTrips(t *testing.T) {
	id := LogID{TenantID: "acme", Database: "ledger", Log: "certs"}
	cases := map[string]struct {
		err     interface{ Metadata() map[string]string }
		rebuild func(map[string]string) error
	}{
		"log exists":         {&LogExistsError{ID: id}, func(md map[string]string) error { return LogExistsFromMetadata(md) }},
		"checkpoint exists":  {&CheckpointExistsError{Size: 1<<64 - 1}, func(md map[string]string) error { return CheckpointExistsFromMetadata(md) }},
		"expire not allowed": {&ExpireNotAllowedError{ID: id}, func(md map[string]string) error { return ExpireNotAllowedFromMetadata(md) }},
	}
	for name, c := range cases {
		if got := c.rebuild(c.err.Metadata()); !reflect.DeepEqual(got, c.err) {
			t.Errorf("%s: rebuilt %#v, want %#v", name, got, c.err)
		}
	}
}

// TestRefusalMetadata_MissingKeysKeepTheType checks the reason, not the
// metadata, carries the refusal: metadata that lost its keys or carries a
// malformed size still rebuilds the typed error, with zero fields.
func TestRefusalMetadata_MissingKeysKeepTheType(t *testing.T) {
	for _, md := range []map[string]string{nil, {refusalKeySize: "not-a-number"}, {refusalKeySize: "-1"}} {
		if got := CheckpointExistsFromMetadata(md); got.Size != 0 {
			t.Errorf("CheckpointExistsFromMetadata(%v) = %+v, want size 0", md, got)
		}
		if got := LogExistsFromMetadata(md); got.ID != (LogID{}) {
			t.Errorf("LogExistsFromMetadata(%v) = %+v, want the zero LogID", md, got)
		}
		if got := ExpireNotAllowedFromMetadata(md); got.ID != (LogID{}) {
			t.Errorf("ExpireNotAllowedFromMetadata(%v) = %+v, want the zero LogID", md, got)
		}
	}
}

func TestRefusalReasons_AreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range []string{ReasonLogExists, ReasonCheckpointExists, ReasonExpireNotAllowed} {
		if r == "" || seen[r] {
			t.Fatalf("reason %q is empty or repeated", r)
		}
		seen[r] = true
	}
}
