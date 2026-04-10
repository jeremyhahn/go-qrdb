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

// HubClosedError is returned when an operation is attempted on a closed hub.
type HubClosedError struct{}

func (e *HubClosedError) Error() string {
	return "watch hub is closed"
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
