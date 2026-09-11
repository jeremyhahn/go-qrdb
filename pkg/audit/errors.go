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

// WriteError is returned when writing an audit entry to a backend fails.
type WriteError struct {
	Backend string
	Err     error
}

// Error returns a human-readable description of the write failure.
func (e *WriteError) Error() string {
	return "audit: write to " + e.Backend + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *WriteError) Unwrap() error { return e.Err }

// FileError is returned when the audit log file cannot be opened or created.
type FileError struct {
	Path string
	Err  error
}

// Error returns a human-readable description of the file operation failure.
func (e *FileError) Error() string {
	return "audit: cannot open file " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *FileError) Unwrap() error { return e.Err }

// ConfigError is returned when the audit configuration is invalid.
type ConfigError struct {
	Detail string
}

// Error returns a human-readable description of the configuration error.
func (e *ConfigError) Error() string {
	return "audit: invalid config: " + e.Detail
}

// ChainBrokenError is returned by VerifyChain when a hash-chain link is broken
// WITHIN one file. Index is the zero-based position of the first mismatching
// event, counting every line the file holds.
//
// Path names the file. It is optional so the message keeps reading correctly
// when only one file is in question, and is set by every verifier in this
// package because a rotation set holds many.
type ChainBrokenError struct {
	Path     string
	Index    int
	Expected string
	Got      string
}

// Error returns a human-readable description of the chain break.
func (e *ChainBrokenError) Error() string {
	msg := "audit: chain broken at index " + itoa(e.Index)
	if e.Path != "" {
		msg += " of " + e.Path
	}
	return msg + ": expected prevHash " + e.Expected + ", got " + e.Got
}

// LinkReason names the way a rotation set failed to form one chain.
type LinkReason string

const (
	// LinkNoPredecessor means a file declares a seed digest no other file
	// hands off -- the archive before it is missing, or was rewritten.
	LinkNoPredecessor LinkReason = "no file in the set hands off to it"

	// LinkForked means two files claim the same predecessor, or two files end
	// on the same digest. A trail is a line, not a tree.
	LinkForked LinkReason = "two files occupy the same position in the chain"

	// LinkMultipleHeads means more than one file is entered by nothing, so
	// the directory holds more than one trail.
	LinkMultipleHeads LinkReason = "the set holds more than one chain"

	// LinkNoHead means every file is entered by another, which for a finite
	// set means the links form a cycle.
	LinkNoHead LinkReason = "the links form a cycle and the set has no head"
)

// ChainLinkError is returned when every file of a rotation set is internally
// sound but the files do not link into one chain.
//
// DISTINCT FROM ChainBrokenError on purpose: that one says a RECORD was
// changed, this one says a FILE was removed, duplicated or reordered. The
// remediation differs and so does the question an operator asks next.
type ChainLinkError struct {
	// Path is the file the fault was observed at; Other is the file it
	// conflicts with, when the reason names two.
	Path  string
	Other string

	// Digest is the hex digest at which the link failed, when the reason has
	// one.
	Digest string

	// Reason names the failure.
	Reason LinkReason
}

// Error returns a human-readable description of the broken linkage.
func (e *ChainLinkError) Error() string {
	msg := "audit: rotation set does not form one chain at " + e.Path
	if e.Other != "" {
		msg += " (conflicts with " + e.Other + ")"
	}
	msg += ": " + string(e.Reason)
	if e.Digest != "" {
		msg += " (digest " + e.Digest + ")"
	}
	return msg
}

// ConcurrentRotationError is returned when a whole-set verification could not
// observe a stable rotation set because the sink rotated underneath it.
//
// IT IS NOT A TAMPER VERDICT and its message says so. The verification made no
// finding; re-running it is the remedy. Reporting a broken chain from a race
// would be worse than reporting nothing, because an operator who learns the
// signal fires on healthy trails stops reading it.
type ConcurrentRotationError struct {
	Path     string
	Attempts int
}

// Error returns a human-readable description of the unstable read.
func (e *ConcurrentRotationError) Error() string {
	return "audit: could not verify " + e.Path + ": the trail rotated during each of " +
		itoa(e.Attempts) + " attempts, so no consistent view was obtained. " +
		"Nothing is known to be wrong with it; re-run the verification."
}

// FileLockedError is returned when the audit file cannot be opened because
// another process holds an exclusive lock.
type FileLockedError struct {
	Path string
}

// Error returns a human-readable description of the lock conflict.
func (e *FileLockedError) Error() string {
	return "audit: file locked: " + e.Path
}

// RetentionError is returned when the retention pruning scan fails.
type RetentionError struct {
	Err error
}

// Error returns a human-readable description of the retention failure.
func (e *RetentionError) Error() string { return "audit: retention failure: " + e.Err.Error() }

// Unwrap returns the underlying error.
func (e *RetentionError) Unwrap() error { return e.Err }

// itoa converts an int to its decimal string representation without
// importing strconv to keep the errors package lightweight.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
