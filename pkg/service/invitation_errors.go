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

package service

import "strconv"

// InvitationFieldError is returned when an invite or redeem request is
// missing a required field or carries one it may not.
type InvitationFieldError struct {
	Field  string
	Reason string
}

// Error names the field and why it was refused.
func (e *InvitationFieldError) Error() string {
	return "service: invitation request field " + e.Field + " " + e.Reason
}

// InvitationInvalidError is the ONE answer to a token that cannot be
// redeemed: unknown, issued for another tenant, expired, already used, or
// for an account that is no longer awaiting its first credential. It says
// nothing about which, because an unauthenticated caller told that a token
// exists but is spent learns that someone was invited.
type InvitationInvalidError struct{}

// Error describes the refusal without distinguishing its cause.
func (e *InvitationInvalidError) Error() string {
	return "service: the invitation is invalid, expired or already used"
}

// InvitationKeyInUseError is returned when the key a redeemer presents is
// already bound to an account of the tenant. An invitation enrols a key of
// the invited user's own: one that already logs in as somebody -- the
// inviter, say -- is refused before the token is spent.
type InvitationKeyInUseError struct{}

// Error describes the refusal.
func (e *InvitationKeyInUseError) Error() string {
	return "service: the key is already bound to an account; an invitation enrols a new key of the invited user's own"
}

// InvitationAccountActiveError is returned when re-inviting an account that
// already has a credential or is not pending: there is nothing left for an
// invitation to activate.
type InvitationAccountActiveError struct {
	Username string
}

// Error names the account.
func (e *InvitationAccountActiveError) Error() string {
	return "service: account " + e.Username + " is not awaiting its first credential"
}

// InvitationUndoFailedError is returned when an invitation could not be
// issued for an account InviteUser had just created, and deleting that
// account failed too: UserID is a pending account no invitation can reach,
// which an operator must delete.
type InvitationUndoFailedError struct {
	UserID  uint64
	Err     error
	UndoErr error
}

// Error describes both failures.
func (e *InvitationUndoFailedError) Error() string {
	return "service: issuing the invitation failed (" + e.Err.Error() + ") and deleting the pending account " +
		strconv.FormatUint(e.UserID, 10) + " it was for failed too (" + e.UndoErr.Error() + "); delete it by hand"
}

// Unwrap returns the issuing failure.
func (e *InvitationUndoFailedError) Unwrap() error { return e.Err }
