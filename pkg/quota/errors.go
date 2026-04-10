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

package quota

import (
	"errors"
	"strconv"
)

// ErrNilChecker is returned when a nil Checker is used where a valid
// Checker is required.
var ErrNilChecker = errors.New("quota: nil checker")

// ExceededError is returned when a tenant attempts to create a resource
// that would exceed their configured quota limit.
type ExceededError struct {
	TenantID string
	Resource Resource
	Current  int64
	Limit    int64
}

// Error returns a human-readable description of the quota violation.
func (e *ExceededError) Error() string {
	return "quota: exceeded for tenant " + e.TenantID +
		": " + e.Resource.String() +
		" at " + strconv.FormatInt(e.Current, 10) +
		"/" + strconv.FormatInt(e.Limit, 10)
}

// InvalidResourceError is returned when an unknown resource type is
// queried for its quota limit.
type InvalidResourceError struct {
	Resource Resource
}

// Error returns a human-readable description of the unknown resource.
func (e *InvalidResourceError) Error() string {
	return "quota: unknown resource type: " + strconv.Itoa(int(e.Resource))
}

// UnregisteredCounterError is returned when a counter function has not
// been registered for the requested resource type.
type UnregisteredCounterError struct {
	Resource Resource
}

// Error returns a human-readable description of the missing counter.
func (e *UnregisteredCounterError) Error() string {
	return "quota: no counter registered for resource: " + e.Resource.String()
}
