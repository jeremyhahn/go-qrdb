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

package permission

// InvalidPermissionError is returned when a permission string does not
// conform to the required "resource:action" format.
type InvalidPermissionError struct {
	Value string
}

// Error returns a human-readable description of the malformed permission.
func (e *InvalidPermissionError) Error() string {
	return "permission: invalid format: " + e.Value + " (expected resource:action)"
}

// UnregisteredPermissionError is returned when an operation references a
// permission that has not been registered in the global registry.
type UnregisteredPermissionError struct {
	Permission Permission
}

// Error returns a human-readable description of the unregistered permission.
func (e *UnregisteredPermissionError) Error() string {
	return "permission: not registered: " + string(e.Permission)
}
