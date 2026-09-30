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

package auth

// The permitted-authentication-method vocabulary.
//
// A user's permitted-method policy (user.User.AuthMethods) is a list of these
// tokens, and CheckAuthMethod in auth/middleware compares the method that
// authenticated a request against it. qrdb defines the tokens so that every
// layer running a login ceremony spells a method the same way: two spellings
// of one method are two permissions, and a policy written with one silently
// refuses the other.
//
// MethodMTLS, the token the built-in mTLS authenticator reports, is declared
// beside that authenticator in mtls.go. The two below name ceremonies qrdb
// does not run itself; a composing host runs them and enforces the policy
// through CheckAuthMethod.
//
// A method is a WAY IN, not an enrolled factor. AuthFactorPIV and
// AuthFactorFIDO2 record what an account has enrolled, and the policy is kept
// separate from them on purpose (see user.User.AuthMethods). That is also why
// there is no "fido2" method: a FIDO2 authenticator is presented through the
// WebAuthn ceremony, so MethodWebAuthn is its one spelling.
const (
	// MethodPIV is the token for a PIV smart-card login. It is distinct from
	// MethodMTLS even though both prove possession of a certificate key: a
	// PIV login is a challenge-response ceremony against an enrolled card,
	// and sharing one token would make a policy that permits one permit both.
	MethodPIV = "piv"

	// MethodWebAuthn is the token for a WebAuthn (FIDO2 authenticator) login.
	MethodWebAuthn = "webauthn"
)
