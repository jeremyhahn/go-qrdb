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

// ABACPolicySpec contains the policy specification (subjects, variables,
// rules).
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

// DecorateAuthorizer wraps an authorizer with ABAC policy evaluation:
// explicit deny overrides the inner allow, explicit allow grants beyond
// the inner authorizer, and unmatched requests delegate to it. With zero
// policies configured the inner authorizer is returned unchanged.
var DecorateAuthorizer = abac.Decorate

// ABACWithPolicyDir loads YAML policy files from a directory at
// construction time.
var ABACWithPolicyDir = abac.WithPolicyDir

// ABACWithPolicies injects programmatically constructed policies.
var ABACWithPolicies = abac.WithPolicies

// LoadABACPolicyDir loads and validates every policy document from the
// YAML files directly inside a directory.
var LoadABACPolicyDir = abac.LoadDir

// LoadABACPolicyFile loads and validates the policy documents contained in
// a single YAML file.
var LoadABACPolicyFile = abac.LoadFile
