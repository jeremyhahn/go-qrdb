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

package dbconv

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
)

// RaftConfig fills every field of the protobuf message from a full request,
// so no field the message carries is dropped on the way out.
func TestRaftConfig_FillsEveryField(t *testing.T) {
	dbwiretest.AssertEveryFieldSet(t, RaftConfig(dbwiretest.FullRaftRequest()))
}

func TestRaftConfig_NilIsNil(t *testing.T) {
	if got := RaftConfig(nil); got != nil {
		t.Fatalf("RaftConfig(nil) = %+v, want nil", got)
	}
}
