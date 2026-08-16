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

package wiring

// MissingServiceError is returned when a caller requests enforced
// authorization or authentication (AuthMiddlewareConfig.Enabled == true, or
// a direct BuildAuthorizer call) but did not supply a required service. It
// signals a fail-loud boot abort rather than an embedder silently serving
// every request unauthenticated because a dependency was never wired.
type MissingServiceError struct {
	// Service names the missing dependency ("role" or "user").
	Service string
}

// Error returns a human-readable description of the missing service.
func (e *MissingServiceError) Error() string {
	return "auth/wiring: auth enforcement is enabled but the " + e.Service +
		" service was not supplied; refusing to serve unauthenticated"
}
