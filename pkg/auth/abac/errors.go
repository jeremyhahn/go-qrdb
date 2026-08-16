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

package abac

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// InvalidPolicyError is returned when a policy document fails validation.
type InvalidPolicyError struct {
	// Name is the policy name; empty when the name itself is missing.
	Name string

	// Reason describes why the policy is invalid.
	Reason string

	// Err is the underlying rule validation error, if any.
	Err error
}

// Error returns a human-readable description of the validation failure.
func (e *InvalidPolicyError) Error() string {
	msg := "qrdb/auth/abac: invalid policy"
	if e.Name != "" {
		msg += " " + strconv.Quote(e.Name)
	}
	msg += ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *InvalidPolicyError) Unwrap() error { return e.Err }

// InvalidRuleError is returned when a policy rule fails validation.
type InvalidRuleError struct {
	// Rule is the rule name; empty when the name itself is missing.
	Rule string

	// Reason describes why the rule is invalid.
	Reason string
}

// Error returns a human-readable description of the rule validation failure.
func (e *InvalidRuleError) Error() string {
	msg := "qrdb/auth/abac: invalid rule"
	if e.Rule != "" {
		msg += " " + strconv.Quote(e.Rule)
	}
	return msg + ": " + e.Reason
}

// CompilationFailedError is returned when a policy cannot be compiled into
// its optimized evaluation form.
type CompilationFailedError struct {
	// Policy is the name of the policy that failed to compile.
	Policy string

	// Reason describes the compilation failure.
	Reason string

	// Err is the underlying compilation error, if any.
	Err error
}

// Error returns a human-readable description of the compilation failure.
func (e *CompilationFailedError) Error() string {
	msg := "qrdb/auth/abac: failed to compile policy " + strconv.Quote(e.Policy) + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *CompilationFailedError) Unwrap() error { return e.Err }

// ConditionParseError is returned when a condition expression cannot be
// parsed.
type ConditionParseError struct {
	// Expression is the condition expression that failed to parse.
	Expression string

	// Reason describes the parse failure.
	Reason string
}

// Error returns a human-readable description of the parse failure.
func (e *ConditionParseError) Error() string {
	return "qrdb/auth/abac: failed to parse condition " + strconv.Quote(e.Expression) + ": " + e.Reason
}

// AttributeMatcherError is returned when a subject attribute matcher uses
// an unknown operator or cannot be compiled.
type AttributeMatcherError struct {
	// Key is the attribute key whose matcher is invalid.
	Key string

	// Err is the underlying error, if any.
	Err error
}

// Error returns a human-readable description of the matcher failure.
func (e *AttributeMatcherError) Error() string {
	msg := "qrdb/auth/abac: invalid attribute matcher for key " + strconv.Quote(e.Key)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *AttributeMatcherError) Unwrap() error { return e.Err }

// PolicyFileError is returned when a policy file cannot be read, parsed,
// or validated.
type PolicyFileError struct {
	// Path is the file path that failed to load.
	Path string

	// Err is the underlying filesystem, YAML, or validation error.
	Err error
}

// Error returns a human-readable description of the file load failure.
func (e *PolicyFileError) Error() string {
	return "qrdb/auth/abac: failed to load policy file " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *PolicyFileError) Unwrap() error { return e.Err }

// PolicyDirError is returned when a policy directory cannot be read.
type PolicyDirError struct {
	// Dir is the directory path that failed to load.
	Dir string

	// Err is the underlying filesystem error.
	Err error
}

// Error returns a human-readable description of the directory load failure.
func (e *PolicyDirError) Error() string {
	return "qrdb/auth/abac: failed to load policy directory " + strconv.Quote(e.Dir) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *PolicyDirError) Unwrap() error { return e.Err }

// NilAuthorizerError is returned by Decorate when the inner authorizer is
// nil. The decorator delegates unmatched requests to the inner authorizer,
// so a nil inner would leave the enforcement path open.
type NilAuthorizerError struct{}

// Error returns a human-readable description of the misconfiguration.
func (e *NilAuthorizerError) Error() string {
	return "qrdb/auth/abac: inner authorizer must not be nil"
}

// PolicyDeniedError is returned when an explicit ABAC deny rule matches a
// request. It names the policy and rule that produced the denial and wraps
// auth.ErrUnauthorized so callers using errors.Is can match the sentinel,
// mirroring rbac.PermissionDeniedError.
type PolicyDeniedError struct {
	// Policy is the name of the policy whose rule denied the request.
	Policy string

	// Rule is the name of the deny rule that matched.
	Rule string

	// Subject is the denied principal.
	Subject string

	// Resource is the resource the subject attempted to access.
	Resource string

	// Action is the action the subject attempted to perform.
	Action string

	// TenantID is the target tenant the request was evaluated against.
	TenantID string
}

// Error returns a human-readable description of the policy denial.
func (e *PolicyDeniedError) Error() string {
	return "qrdb/auth/abac: permission denied: " + e.Subject + " cannot " +
		e.Action + " on " + e.Resource + ": denied by policy " +
		strconv.Quote(e.Policy) + ", rule " + strconv.Quote(e.Rule)
}

// Unwrap returns auth.ErrUnauthorized so errors.Is works with the sentinel.
func (e *PolicyDeniedError) Unwrap() error { return auth.ErrUnauthorized }

// EvaluationError is returned when policy evaluation itself fails. The
// decorator fails closed on evaluation errors: the request is rejected
// rather than silently delegated.
type EvaluationError struct {
	// Err is the underlying evaluation error.
	Err error
}

// Error returns a human-readable description of the evaluation failure.
func (e *EvaluationError) Error() string {
	return "qrdb/auth/abac: policy evaluation failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *EvaluationError) Unwrap() error { return e.Err }
