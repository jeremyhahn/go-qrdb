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

// ChainBrokenError is returned by VerifyChain when a hash-chain link is broken.
// Index is the zero-based position of the first mismatching event.
type ChainBrokenError struct {
	Index    int
	Expected string
	Got      string
}

// Error returns a human-readable description of the chain break.
func (e *ChainBrokenError) Error() string {
	return "audit: chain broken at index " + itoa(e.Index) +
		": expected prevHash " + e.Expected + ", got " + e.Got
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
