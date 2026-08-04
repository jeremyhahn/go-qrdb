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

package grpc

// dialError is returned when gRPC dial fails.
type dialError struct {
	Err error
}

// Error returns a human-readable description.
func (e *dialError) Error() string {
	return "grpc transport: dial failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *dialError) Unwrap() error {
	return e.Err
}
