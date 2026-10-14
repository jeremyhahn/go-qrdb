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

//go:build clusterproof

package history

import "strconv"

// InvalidOpError is a record that breaks the history format.
type InvalidOpError struct {
	ID     uint64
	Reason string
}

func (e *InvalidOpError) Error() string {
	return "history: op " + strconv.FormatUint(e.ID, 10) + ": " + e.Reason
}

// DecodeError is a line of a serialised history that could not be read.
// Line is 1-based, or 0 when the reader itself failed.
type DecodeError struct {
	Line int
	Err  error
}

func (e *DecodeError) Error() string {
	if e.Line == 0 {
		return "history: read: " + e.Err.Error()
	}
	return "history: line " + strconv.Itoa(e.Line) + ": " + e.Err.Error()
}

func (e *DecodeError) Unwrap() error { return e.Err }

// EncodeError is a record that could not be written.
type EncodeError struct {
	ID  uint64
	Err error
}

func (e *EncodeError) Error() string {
	return "history: encode op " + strconv.FormatUint(e.ID, 10) + ": " + e.Err.Error()
}

func (e *EncodeError) Unwrap() error { return e.Err }
