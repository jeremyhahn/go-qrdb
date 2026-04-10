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

package consistency

import "strconv"

// UnknownLevelError is returned when an unrecognized consistency level
// string is provided.
type UnknownLevelError struct {
	Level string
}

func (e *UnknownLevelError) Error() string {
	return "unknown consistency level: " + e.Level + " (valid: strong, linearizable, lease_read, eventual)"
}

// InvalidLevelError is returned when a consistency level value is out
// of the valid range.
type InvalidLevelError struct {
	Level Level
}

func (e *InvalidLevelError) Error() string {
	return "invalid consistency level: " + e.Level.String()
}

// InvalidTimeoutError is returned when a negative timeout is provided
// in read options.
type InvalidTimeoutError struct {
	Timeout int64
}

func (e *InvalidTimeoutError) Error() string {
	return "timeout must be non-negative, got: " + strconv.FormatInt(e.Timeout, 10)
}
