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

package errclass

import (
	"errors"
	"reflect"
	"slices"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// rule is one row of the classification table: a way of recognising an error,
// and the class it is.
//
// samples are the values the parity tests feed to every transport. A row
// built with as or is supplies its own; a row with a custom matcher states
// them explicitly, because a row no test can exercise is a row nothing checks.
type rule struct {
	name     string
	classify func(error) (Class, bool)
	samples  []error
}

// as is a row that recognises the dynamic type of sample anywhere in err's
// chain.
//
// The sample carries the whole row: `as(&service.KeyNotFoundError{}, NotFound)`
// names the type once and doubles as the value the parity tests feed every
// transport, so adding a condition is a one-line change to one table.
//
// The match target is built from the sample's type by reflection, so a row is
// a plain value expression with no instantiation of its own; errors.As is
// reflective underneath either way.
func as(sample error, c Class) rule {
	target := reflect.TypeOf(sample)
	return rule{
		name: target.String(),
		classify: func(err error) (Class, bool) {
			// A fresh target per call: errors.As writes through it, and
			// Classify runs concurrently on every request path.
			return c, errors.As(err, reflect.New(target).Interface())
		},
		samples: []error{sample},
	}
}

// is is a row that recognises a sentinel anywhere in err's chain.
func is(sentinel error, c Class) rule {
	return rule{
		name: sentinel.Error(),
		classify: func(err error) (Class, bool) {
			return c, errors.Is(err, sentinel)
		},
		samples: []error{sentinel},
	}
}

// classedRule recognises an error that declares its own class (see Classed).
var classedRule = rule{
	name: "errclass.Classed",
	classify: func(err error) (Class, bool) {
		var classed Classed
		if !errors.As(err, &classed) {
			return Unclassified, false
		}
		return classed.ErrorClass(), true
	},
	// Classed types live in the transport packages; each of those packages'
	// tests feeds its own types through this row.
}

// authClassByFailure maps the authentication classifier's classes onto this
// table's. AuthFailureUnknown is absent on purpose: an error the auth
// classifier does not recognise falls through to the rest of the table.
var authClassByFailure = map[authmw.AuthFailureClass]Class{
	authmw.AuthFailureUnauthenticated:  Unauthenticated,
	authmw.AuthFailureUnauthorized:     PermissionDenied,
	authmw.AuthFailureUnavailable:      Unavailable,
	authmw.AuthFailureRateLimited:      ResourceExhausted,
	authmw.AuthFailureAuditUnavailable: Unavailable,
}

// authRule delegates to pkg/auth/middleware.ClassifyAuthError, which stays the
// owner of the authentication and authorization failures: its classes also
// drive control flow (the bootstrap fallbacks tell an audit outage from a
// sealed tenant) that a wire class cannot express.
var authRule = rule{
	name: "middleware.ClassifyAuthError",
	classify: func(err error) (Class, bool) {
		c, ok := authClassByFailure[authmw.ClassifyAuthError(err)]
		return c, ok
	},
	samples: authSamples,
}

// rules is the classification table, first match wins.
//
// A slice rather than a map keyed by type, for the reason the auth classifier
// gives: errors.As has to run against a typed target, and a wrapper error
// makes ORDER meaningful — an *IndexCompensationError unwraps to the conflict
// that caused it, and must be reported as the failed rollback it is rather
// than as its cause. So the table is ordered in three bands:
//
//  1. dominantRules — conditions that must win however deeply they are
//     wrapped (a closed store, a sealed tenant, a failed rollback, an
//     authorization refusal).
//  2. the per-family blocks — leaf conditions, one block per domain, so a
//     change to one family is a one-line edit to one block.
//  3. genericRules — the wrappers a failure is reported through ("propose
//     failed", "store error"). They come LAST so they can only ever classify
//     what nothing more specific recognised.
//
// TestRulesAreNotShadowed feeds every row's samples back through Classify and
// fails if any row is answered by an earlier one, so a row that can never
// fire is a test failure rather than a silent no-op.
var rules = slices.Concat(
	dominantRules,
	dataPlaneRules,
	vectorRules,
	collectionRules,
	importRules,
	restoreRules,
	databaseRules,
	placementRules,
	lockRules,
	sqlRules,
	catalogRules,
	entityRules,
	privilegeRules,
	ragRules,
	embeddingRules,
	backupRules,
	clusterJoinTokenRules,
	onboardingRules,
	identityRules,
	volumeRules,
	tenantRules,
	holderRules,
	credentialRules,
	dekRules,
	auditVerifyRules,
	pinRules,
	authFactorRules,
	userAuthFactorRules,
	ceremonyRules,
	migrationRules,
	contextRules,
	genericRules,
)

// Classify reports what kind of failure err represents.
//
// A nil error is Unclassified: there is no failure to classify, and a caller
// that reaches a mapper with nil has a bug this function must not paper over
// by inventing a success class.
func Classify(err error) Class {
	if err == nil {
		return Unclassified
	}
	for _, r := range rules {
		if c, ok := r.classify(err); ok {
			return c
		}
	}
	return Unclassified
}
