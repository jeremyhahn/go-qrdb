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

import (
	"errors"
	"strconv"
)

// ErrFactorConditionFailed is returned by a conditional factor add whose
// condition did not hold when the add was decided. The store is unchanged:
// no factor was enrolled by that call.
var ErrFactorConditionFailed = errors.New("auth: factor add condition failed")

// FactorAddCondition is the guard on a conditional factor add. The add
// enrolls the factor only when every condition that is set holds against the
// user's factor set at the moment the add commits; at least one must be set.
//
// The decision is made against replicated state, not by a read the caller
// made earlier, so two callers racing the same condition cannot both win.
type FactorAddCondition struct {
	// NoActiveOfType refuses the add when the user already holds an active
	// factor of the type being added. It is the one-time claim: of N callers
	// enrolling the same kind of factor for the same user, exactly one wins.
	NoActiveOfType bool

	// ExpectedVersion, when non-nil, refuses the add unless the user's
	// factor-set version equals it. The version is the one List reports;
	// it moves every time a factor joins or leaves the set, so an add
	// guarded on it succeeds only if nothing changed since the caller looked.
	// A user whose factor set was never changed is at version 0.
	ExpectedVersion *uint64
}

// Validate reports whether c names at least one condition. An empty
// condition is refused rather than treated as an unconditional add, so a
// caller that meant to guard an add and dropped the guard learns of it.
func (c FactorAddCondition) Validate() error {
	if !c.NoActiveOfType && c.ExpectedVersion == nil {
		return &InvalidFactorError{Reason: "factor add condition names no condition"}
	}
	return nil
}

// FactorAddConditionOf returns the condition a request's wire fields name,
// or nil when they name none -- an unconditional add. Every transport builds
// its condition through it, so the same fields mean the same add on each.
func FactorAddConditionOf(noActiveOfType bool, expectedVersion *uint64) *FactorAddCondition {
	if !noActiveOfType && expectedVersion == nil {
		return nil
	}
	return &FactorAddCondition{NoActiveOfType: noActiveOfType, ExpectedVersion: expectedVersion}
}

// RefuseRotationCondition returns an *InvalidFactorError when a rotation's
// new factor names an add condition, and nil when it names none. A rotation
// is not conditional; a guard it dropped would let the caller believe the
// rotation had been decided by one.
func RefuseRotationCondition(cond *FactorAddCondition) error {
	if cond != nil {
		return &InvalidFactorError{Reason: "a rotation's new factor cannot carry an add condition"}
	}
	return nil
}

// FactorConditionKind names which condition of a FactorAddCondition failed.
type FactorConditionKind string

const (
	// FactorConditionNoActiveOfType is a NoActiveOfType condition refused
	// because the user holds an active factor of the type.
	FactorConditionNoActiveOfType FactorConditionKind = "no_active_of_type"

	// FactorConditionExpectedVersion is an ExpectedVersion condition refused
	// because the factor-set version had moved.
	FactorConditionExpectedVersion FactorConditionKind = "expected_version"
)

// FactorConditionFailedError is returned by a conditional factor add whose
// condition did not hold. It unwraps to ErrFactorConditionFailed.
type FactorConditionFailedError struct {
	// UserID is the user the add targeted.
	UserID uint64
	// Type is the factor type the add would have enrolled.
	Type AuthFactorType
	// Condition is the condition that failed.
	Condition FactorConditionKind
	// ExpectedVersion is the version the caller expected; meaningful only
	// when Condition is FactorConditionExpectedVersion.
	ExpectedVersion uint64
	// CurrentVersion is the factor-set version the add was decided against.
	CurrentVersion uint64
	// ExistingFactorID is the active factor of Type that refused a
	// NoActiveOfType condition.
	ExistingFactorID string
}

// Error implements error.
func (e *FactorConditionFailedError) Error() string {
	if e.Condition == FactorConditionExpectedVersion {
		return "auth: factor add condition failed: factor-set version is " +
			strconv.FormatUint(e.CurrentVersion, 10) + ", expected " +
			strconv.FormatUint(e.ExpectedVersion, 10)
	}
	return "auth: factor add condition failed: user already holds an active " +
		string(e.Type) + " factor " + e.ExistingFactorID
}

// Unwrap returns the sentinel error so errors.Is succeeds.
func (e *FactorConditionFailedError) Unwrap() error { return ErrFactorConditionFailed }

// ReasonFactorConditionFailed is the structured-refusal reason every
// transport carries beside the message of a *FactorConditionFailedError, so
// a client rebuilds the typed error -- and errors.Is matches the sentinel --
// without parsing text.
const ReasonFactorConditionFailed = "FACTOR_CONDITION_FAILED"

// Refusal metadata keys of a *FactorConditionFailedError.
const (
	conditionKeyUserID          = "user_id"
	conditionKeyType            = "factor_type"
	conditionKeyCondition       = "condition"
	conditionKeyExpectedVersion = "expected_version"
	conditionKeyCurrentVersion  = "current_version"
	conditionKeyExistingFactor  = "existing_factor_id"
)

// Metadata encodes e as structured-refusal metadata.
func (e *FactorConditionFailedError) Metadata() map[string]string {
	return map[string]string{
		conditionKeyUserID:          strconv.FormatUint(e.UserID, 10),
		conditionKeyType:            string(e.Type),
		conditionKeyCondition:       string(e.Condition),
		conditionKeyExpectedVersion: strconv.FormatUint(e.ExpectedVersion, 10),
		conditionKeyCurrentVersion:  strconv.FormatUint(e.CurrentVersion, 10),
		conditionKeyExistingFactor:  e.ExistingFactorID,
	}
}

// FactorConditionFailedFromMetadata rebuilds the error Metadata encoded. It
// always returns an error matching ErrFactorConditionFailed: the reason, not
// the metadata, is what says the condition failed, so a missing or malformed
// detail key leaves that field zero rather than losing the classification.
func FactorConditionFailedFromMetadata(md map[string]string) *FactorConditionFailedError {
	return &FactorConditionFailedError{
		UserID:           metadataUint(md, conditionKeyUserID),
		Type:             AuthFactorType(md[conditionKeyType]),
		Condition:        FactorConditionKind(md[conditionKeyCondition]),
		ExpectedVersion:  metadataUint(md, conditionKeyExpectedVersion),
		CurrentVersion:   metadataUint(md, conditionKeyCurrentVersion),
		ExistingFactorID: md[conditionKeyExistingFactor],
	}
}

// metadataUint parses md[key] as a uint64, zero when absent or malformed.
func metadataUint(md map[string]string, key string) uint64 {
	n, err := strconv.ParseUint(md[key], 10, 64)
	if err != nil {
		return 0
	}
	return n
}
