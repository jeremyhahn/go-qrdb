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

// ReadOptions contains options for read operations.
type ReadOptions struct {
	// Consistency specifies the consistency level for this read.
	Consistency Level

	// Timeout specifies the maximum time in milliseconds to wait for
	// the read to complete. If zero, a default timeout is used.
	Timeout int64

	// AllowStale explicitly allows stale reads.
	// Only effective when Consistency is Eventual.
	AllowStale bool
}

// DefaultReadOptions returns read options with default values.
func DefaultReadOptions() ReadOptions {
	return ReadOptions{
		Consistency: Linearizable,
		Timeout:     5000,
	}
}

// Validate checks if the read options are valid.
func (opts *ReadOptions) Validate() error {
	if !opts.Consistency.IsValid() {
		return &InvalidLevelError{Level: opts.Consistency}
	}
	if opts.Timeout < 0 {
		return &InvalidTimeoutError{Timeout: opts.Timeout}
	}
	return nil
}
