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

// Package holderprofile names the holder certificate profiles. The ceremony
// CA (pkg/ceremony) stamps one of them as the second Subject OU of every
// Security Officer and share-holder certificate it signs, after the tenant:
// OU = [<tenant>, <profile>].
//
// A holder certificate proves who holds a share; it is not a login. pkg/auth
// recognises it and never resolves it to a user account, and pkg/tenant
// reserves the names as tenant ids. It is a leaf package -- it imports
// nothing from this module -- so both can use it without importing each
// other.
package holderprofile

const (
	// SecurityOfficer marks a Security Officer certificate.
	SecurityOfficer = "security-officer"

	// Custodian marks an officer or custodian share-holder certificate.
	Custodian = "custodian"
)

// profiles is the set Is recognises.
var profiles = map[string]struct{}{
	SecurityOfficer: {},
	Custodian:       {},
}

// Is reports whether name is a holder certificate profile.
func Is(name string) bool {
	_, ok := profiles[name]
	return ok
}
