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

package service

// CompactionStateQuery reads one scope's scheduled-compaction state from the
// MVCC state machine's reserved compaction table. The answer is a
// *compaction.State, nil when the scope has no record.
type CompactionStateQuery struct {
	// Scope is the record's scope (compaction.RecordKey).
	Scope string
}

// CompactionStatesQuery reads every scope's scheduled-compaction state from
// the MVCC state machine's reserved compaction table. The answer is a
// map[string]*compaction.State keyed by scope.
type CompactionStatesQuery struct{}
