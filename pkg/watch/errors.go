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

package watch

import (
	"errors"
	"strconv"
)

// ErrWatcherOverflow indicates a watcher's event buffer filled because the
// consumer could not keep up. The hub evicts such a watcher (cancelling it)
// rather than blocking the state-machine apply loop on delivery; the client
// observes the watch ending with this error and may re-establish it.
var ErrWatcherOverflow = errors.New("qrdb/watch: watcher event buffer overflow; consumer too slow")

// ErrCompacted indicates a watch requested a start revision at or below the
// store's compacted revision, so the intervening history is gone. The client's
// remedy is a fresh range read followed by a watch from the current revision —
// the same contract etcd surfaces as "mvcc: required revision has been
// compacted".
var ErrCompacted = errors.New("qrdb/watch: required start revision has been compacted")

// CompactedError is the ErrCompacted condition with the compaction floor that
// caused it: the floor covering the watched key or range, never another
// namespace's. It matches ErrCompacted under errors.Is and reads as it does.
// The floor is what etcd's watch protocol returns as a response's
// CompactRevision.
type CompactedError struct {
	// CompactRevision is the compaction floor covering the watched key or
	// range when the watch was refused or cancelled.
	CompactRevision int64
}

// Error returns ErrCompacted's text, which clients match on.
func (e *CompactedError) Error() string { return ErrCompacted.Error() }

// Is reports whether target is ErrCompacted.
func (e *CompactedError) Is(target error) bool { return target == ErrCompacted }

// ErrStartRevisionUnsupported indicates a watch requested a start revision on a
// hub that has no history replayer attached (a non-MVCC store), so replay is
// impossible.
var ErrStartRevisionUnsupported = errors.New("qrdb/watch: start revision replay is not supported by this store")

// HubClosedError is returned when an operation is attempted on a closed hub.
type HubClosedError struct{}

func (e *HubClosedError) Error() string {
	return "watch hub is closed"
}

// WatcherLimitError is returned when opening a watcher would exceed the
// node's watch.max_watchers cap. Closing a watcher frees its slot.
type WatcherLimitError struct {
	// Max is the cap that was reached.
	Max uint64
}

// Error implements the error interface.
func (e *WatcherLimitError) Error() string {
	return "qrdb/watch: watcher limit reached (max_watchers " + strconv.FormatUint(e.Max, 10) + ")"
}

// WatchCreateError is returned when creating a watch fails.
type WatchCreateError struct {
	Cause error
}

// Error implements the error interface.
func (e *WatchCreateError) Error() string {
	return "failed to create watch: " + e.Cause.Error()
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *WatchCreateError) Unwrap() error {
	return e.Cause
}
