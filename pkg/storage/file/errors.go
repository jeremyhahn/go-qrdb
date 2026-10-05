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

// KeySegmentTooLongError refuses a key whose encoded form contains a
// '/'-delimited segment too long for this filesystem to name as a single
// path component (NAME_MAX is 255 bytes on every filesystem this engine
// targets; see maxEncodedSegmentLen). It is a property of the KEY, detected
// before any path is opened, never a fault from the filesystem itself — the
// distinction a storage fault and a refused request need (see
// protocol.RefusalError, which this satisfies by having the same method
// shape without importing that package: pkg/storage is a cross-repo,
// protocol-agnostic contract, and must stay one).
type KeySegmentTooLongError struct {
	// Segment is the oversized encoded segment, truncated for the message so
	// a pathological key cannot make the error itself unbounded.
	Segment string
}

// Error returns a human-readable description of the refused key.
func (e *KeySegmentTooLongError) Error() string {
	seg := e.Segment
	if len(seg) > 64 {
		seg = seg[:64] + "..."
	}
	return "file storage: key segment too long for this filesystem (encoded: " + seg + ")"
}

// Refusal declares this a refusal of the request on its merits, not a
// storage fault — see protocol.RefusalError. A caller one layer up classifies
// through the method shape alone; pkg/storage does not import pkg/protocol.
func (e *KeySegmentTooLongError) Refusal() string {
	return "key segment exceeds this filesystem's path component limit"
}
