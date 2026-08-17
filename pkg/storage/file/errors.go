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

package file

// JournalCorruptError indicates the commit journal found at open time is
// unreadable — the last batch commit is in an unknown state, so the store
// fails closed rather than silently serving a half-applied batch.
type JournalCorruptError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *JournalCorruptError) Error() string {
	return "file storage: commit journal corrupt: " + e.Reason
}

// JournalWriteError wraps a failure to publish, apply, or clear the
// commit journal during a batch commit.
type JournalWriteError struct {
	Err error
}

// Error returns a human-readable description.
func (e *JournalWriteError) Error() string {
	return "file storage: commit journal: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *JournalWriteError) Unwrap() error {
	return e.Err
}
