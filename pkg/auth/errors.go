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

package auth

// AuthError is a typed error for authentication/authorization failures.
type AuthError struct {
	Code    string
	Message string
}

// Error implements the error interface.
func (e *AuthError) Error() string {
	return "qrdb/auth: " + e.Code + ": " + e.Message
}

var (
	// ErrUnauthenticated is returned when no valid credentials are found.
	ErrUnauthenticated = &AuthError{Code: "unauthenticated", Message: "no valid credentials provided"}

	// ErrUnauthorized is returned when the identity lacks permission.
	ErrUnauthorized = &AuthError{Code: "unauthorized", Message: "insufficient permissions"}

	// ErrNoPeerCertificate is returned when mTLS is expected but no client certificate is present.
	ErrNoPeerCertificate = &AuthError{Code: "no_peer_cert", Message: "no client certificate in TLS connection"}

	// ErrInvalidCertificate is returned when the client certificate is malformed or unverified.
	ErrInvalidCertificate = &AuthError{Code: "invalid_cert", Message: "client certificate is invalid or unverified"}
)
