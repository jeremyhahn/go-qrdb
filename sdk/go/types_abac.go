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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
)

// ---------------------------------------------------------------------------
// ABAC authorization decorator
//
// These aliases re-export the canonical ABAC engine owned by
// github.com/jeremyhahn/go-qrdb/pkg/auth/abac so downstream composers
// (go-xkms, go-quicbaas) can build policies and decorate authorizers while
// consuming only sdk/go, never go-qrdb/pkg/*.
// ---------------------------------------------------------------------------

// ABACPolicy is a declarative ABAC policy document
// (apiVersion / kind / metadata / spec).
type ABACPolicy = abac.Policy

// ABACPolicyMetadata contains policy identification and tenant scoping.
type ABACPolicyMetadata = abac.PolicyMetadata

// ABACPolicySpec contains the policy specification (enabled, subjects,
// variables, rules). Enabled is presence-tracked: nil means enabled, false
// makes the evaluator skip the policy; see EnabledSet and IsEnabled.
type ABACPolicySpec = abac.PolicySpec

// ABACSubjectSelector defines criteria for matching subjects.
type ABACSubjectSelector = abac.SubjectSelector

// ABACRule represents a single authorization rule within a policy.
type ABACRule = abac.Rule

// ABACCondition represents a policy evaluation condition expression.
type ABACCondition = abac.Condition

// ABACEffect represents the authorization decision of a rule.
type ABACEffect = abac.Effect

const (
	// ABACEffectAllow grants access.
	ABACEffectAllow = abac.EffectAllow

	// ABACEffectDeny denies access.
	ABACEffectDeny = abac.EffectDeny
)

// ABACDecoratorOption configures DecorateAuthorizer.
type ABACDecoratorOption = abac.Option

// ABACPolicyDeniedError is returned when an explicit ABAC deny rule
// matches a request. It wraps ErrUnauthorized.
type ABACPolicyDeniedError = abac.PolicyDeniedError

// ABACPolicyReloader is the seam for replacing an ABAC decorator's active
// policy set at runtime, without restarting the embedding process. The
// authorizer returned by DecorateAuthorizer implements this interface
// whenever it wraps at all (i.e. whenever it isn't the bare pass-through
// inner authorizer) — type-assert the result, or use
// sdk/go/node.Node.ABACPolicyReloader for the SDK node's composed
// authorizer.
type ABACPolicyReloader = abac.PolicyReloader

// DecorateAuthorizer wraps an authorizer with ABAC policy evaluation:
// explicit deny overrides the inner allow, explicit allow grants beyond
// the inner authorizer, and unmatched requests delegate to it. With zero
// policies configured (and ABACWithLiveReload not supplied) the inner
// authorizer is returned unchanged.
var DecorateAuthorizer = abac.Decorate

// ABACWithPolicyDir loads YAML policy files from a directory at
// construction time.
var ABACWithPolicyDir = abac.WithPolicyDir

// ABACWithPolicies injects programmatically constructed policies.
var ABACWithPolicies = abac.WithPolicies

// ABACWithLiveReload keeps DecorateAuthorizer's returned authorizer
// reloadable (implementing ABACPolicyReloader) even when zero policies are
// configured at construction time, so an embedder can add its first policy
// later via Reload instead of restarting the process.
var ABACWithLiveReload = abac.WithLiveReload

// LoadABACPolicyDir loads and validates every policy document from the
// YAML files directly inside a directory.
var LoadABACPolicyDir = abac.LoadDir

// LoadABACPolicyFile loads and validates the policy documents contained in
// a single YAML file.
var LoadABACPolicyFile = abac.LoadFile

// ABACConditionParser parses ABAC condition expressions into compiled
// conditions. It is the parser the server evaluates policy conditions with,
// so a consumer validating an expression gets the server's verdict.
type ABACConditionParser = abac.ConditionParser

// ABACCompiledCondition is a parsed condition expression, evaluated against
// an authorization context.
type ABACCompiledCondition = abac.CompiledCondition

// ABACConditionParseError is returned when a condition expression cannot be
// parsed.
type ABACConditionParseError = abac.ConditionParseError

// NewABACConditionParser creates a condition parser holding the given policy
// variables.
var NewABACConditionParser = abac.NewConditionParser
