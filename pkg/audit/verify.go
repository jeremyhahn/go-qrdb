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

package audit

import (
	"context"
	"errors"
)

// Verifier is implemented by AuditLogger backends capable of self-verifying
// their own tamper-evidence hash chain. *ChainFileLogger is the only
// first-party implementation; RingBuffer, NoopLogger, and SlogSink hold no
// chain and do not implement it.
type Verifier interface {
	// VerifyChainSet verifies the backend's rotation set and reports what
	// the walk covered. See ChainFileLogger.VerifyChainSet for the full
	// contract, including the ChainBrokenError / ChainLinkError distinction
	// this package's Verify function relies on.
	VerifyChainSet(ctx context.Context) (*ChainVerification, error)
}

// Compile-time interface check.
var _ Verifier = (*ChainFileLogger)(nil)

// VerificationUnsupportedError is returned by Verify when the configured
// AuditLogger (or, for an aggregate, none of its children) implements
// Verifier -- there is no hash chain to check.
type VerificationUnsupportedError struct{}

// Error returns a human-readable description of the unsupported backend.
func (e *VerificationUnsupportedError) Error() string {
	return "audit: configured logger does not support chain verification"
}

// VerificationReport is the outcome of a Verify call. Verified is the only
// field a caller MUST check; Reason and the ChainVerification fields exist
// to tell an operator what was checked and, when Verified is false, what
// broke.
type VerificationReport struct {
	// Verified is true only when every linked file's chain checked out.
	Verified bool `json:"verified"`

	// Reason is a human-readable description of the break. Empty when
	// Verified is true.
	Reason string `json:"reason,omitempty"`

	// Files is the number of rotation-set files that carry records and
	// were linked.
	Files int `json:"files"`

	// Records is the number of audit records verified, anchors excluded.
	// int64: a trail's record count is unbounded over its retention window.
	Records int64 `json:"records"`

	// Order lists the linked files in chain order, head first.
	Order []string `json:"order,omitempty"`

	// Genesis reports whether the walk reached the chain's origin.
	Genesis bool `json:"genesis"`

	// Seed is the hex digest the head claims to continue from, empty at
	// genesis. Handoff is the hex digest the tail ends on.
	Seed    string `json:"seed,omitempty"`
	Handoff string `json:"handoff,omitempty"`
}

// reportFromVerification builds a VerificationReport from the (possibly
// partial, possibly nil) ChainVerification a Verifier call returned.
func reportFromVerification(cv *ChainVerification, verified bool, reason string) *VerificationReport {
	r := &VerificationReport{Verified: verified, Reason: reason}
	if cv != nil {
		r.Files = cv.Files
		r.Records = cv.Records
		r.Order = cv.Order
		r.Genesis = cv.Genesis
		r.Seed = cv.Seed
		r.Handoff = cv.Handoff
	}
	return r
}

// Verify verifies the given AuditLogger's tamper-evidence hash chain and
// reports the outcome as a VerificationReport, or returns an error when
// verification could not be PERFORMED at all.
//
// The split is deliberate and mirrors ChainVerification's own doc comments:
// a *ChainBrokenError (a record was changed) or *ChainLinkError (a file was
// removed, duplicated, or reordered) is a TAMPER VERDICT, so it is reported
// through VerificationReport.Verified=false, not returned as an error --
// exactly as backup.VerifyBackup reports a checksum mismatch as a result
// field rather than an error. Every other failure -- an unsupported backend,
// a missing file, a concurrent-rotation race that produced no stable view --
// answers no question about the trail's integrity, so it is returned as an
// error instead of being folded into the report, where it would read as a
// tamper verdict to a caller checking only the Verified field.
func Verify(ctx context.Context, l AuditLogger) (*VerificationReport, error) {
	v, ok := l.(Verifier)
	if !ok {
		return nil, &VerificationUnsupportedError{}
	}

	cv, err := v.VerifyChainSet(ctx)
	if err == nil {
		return reportFromVerification(cv, true, ""), nil
	}

	var broken *ChainBrokenError
	var link *ChainLinkError
	if errors.As(err, &broken) || errors.As(err, &link) {
		return reportFromVerification(cv, false, err.Error()), nil
	}

	return nil, err
}
