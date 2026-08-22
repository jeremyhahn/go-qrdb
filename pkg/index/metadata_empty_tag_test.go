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

package index

import (
	"reflect"
	"testing"

	qrdbErrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

type emptyIndexTagEntity struct {
	ID   uint64 `json:"id"`
	Team string `json:"team" index:""`
}

type absentIndexTagEntity struct {
	ID   uint64 `json:"id"`
	Team string `json:"team"`
}

type validIndexTagEntity struct {
	ID   uint64 `json:"id"`
	Team string `json:"team" index:"true"`
}

// `index:""` is a written index that never existed, and it used to say nothing.
//
// reflect.StructTag.Get returns "" both for an ABSENT tag and for an EMPTY one,
// and the skip tested that string — so a field the author tagged registered no
// index. The query planner's catalog reads the same tag the same way, so it
// advertised none either. The halves agreeing is what made it undetectable:
// queries stayed correct, took a full scan forever, and reported nothing.
//
// parseIndexTag already refuses an empty tag; it was simply never reached
// (go-qrdb#57).
func TestParseIndexTags_AnEmptyTagIsAnError(t *testing.T) {
	_, err := ParseIndexTags(reflect.TypeOf(emptyIndexTagEntity{}))
	if err == nil {
		t.Fatal("a field tagged `index:\"\"` parsed cleanly; it registers no index " +
			"and advertises none, so the author's index silently does not exist")
	}
	if got := qrdbErrors.Code(err); got != qrdbErrors.ErrInvalidInput {
		t.Fatalf("error code = %v, want %v", got, qrdbErrors.ErrInvalidInput)
	}
}

// The distinction the fix rests on: a field with NO index tag is not an error,
// it is simply not indexed. Without this, "reject empty" would reject every
// untagged field in the portfolio.
func TestParseIndexTags_AnAbsentTagIsNotAnError(t *testing.T) {
	meta, err := ParseIndexTags(reflect.TypeOf(absentIndexTagEntity{}))
	if err != nil {
		t.Fatalf("a struct with no index tags failed to parse: %v", err)
	}
	if len(meta) != 0 {
		t.Fatalf("got %d index definitions from an untagged struct, want 0", len(meta))
	}
}

// A well-formed tag still produces its index.
func TestParseIndexTags_AValidTagStillParses(t *testing.T) {
	meta, err := ParseIndexTags(reflect.TypeOf(validIndexTagEntity{}))
	if err != nil {
		t.Fatalf("ParseIndexTags: %v", err)
	}
	if _, ok := meta["Team"]; !ok {
		t.Fatalf("no index metadata for the tagged field; got %v", meta)
	}
}
