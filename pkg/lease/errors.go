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

package lease

import "strconv"

// NotFoundError is returned when a lease ID does not exist.
type NotFoundError struct {
	ID int64
}

func (e *NotFoundError) Error() string {
	return "lease not found: " + strconv.FormatInt(e.ID, 10)
}

// ExistsError is returned when attempting to grant a lease with an ID
// that is already in use.
type ExistsError struct {
	ID int64
}

func (e *ExistsError) Error() string {
	return "lease already exists: " + strconv.FormatInt(e.ID, 10)
}

// InvalidTTLError is returned when a lease TTL is invalid.
type InvalidTTLError struct {
	TTL int64
}

func (e *InvalidTTLError) Error() string {
	return "invalid lease TTL: " + strconv.FormatInt(e.TTL, 10) + " (must be > 0)"
}

// LessorClosedError is returned when an operation is attempted on a
// closed lessor.
type LessorClosedError struct{}

func (e *LessorClosedError) Error() string {
	return "lessor is closed"
}
