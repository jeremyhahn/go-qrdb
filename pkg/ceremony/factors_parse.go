// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
//  1. GNU Affero General Public License v3.0 (AGPL-3.0)
//     See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
//  2. Commercial License
//     Contact licensing@automatethethings.com for commercial licensing options.

package ceremony

import (
	"sort"
	"strings"
)

// ParseAdminFactors turns operator-supplied factor names into a plan.
//
// It lives here, not in each binary's CLI, because the whole point is that
// `--admin-factor mtls --admin-factor fido2` means the same thing to xkmsd,
// quicbaasd, and any other daemon in the stack. A per-binary parser is how
// three projects end up with three vocabularies for one concept.
//
// Accepted input is forgiving about presentation and strict about meaning:
// case and surrounding whitespace are normalised, a single value may carry a
// comma-separated list (so `--admin-factor mtls,fido2` works as well as
// repeating the flag), and empty entries are skipped. An unrecognised name is
// an error naming what IS accepted — never a silent skip, because an operator
// who asked for a passkey and got only a certificate would not find out until
// the passkey failed to work.
func ParseAdminFactors(values []string) (*EnrollmentPlan, error) {
	requested := make([]AdminFactor, 0, len(values))
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			name := strings.ToLower(strings.TrimSpace(part))
			if name == "" {
				continue
			}
			factor := AdminFactor(name)
			if !IsEnrollableAdminFactor(factor) {
				return nil, &UnsupportedFactorError{Factor: factor}
			}
			requested = append(requested, factor)
		}
	}
	return NewEnrollmentPlan(requested)
}

// AdminFactorNames lists the accepted --admin-factor values, sorted, for flag
// help text and error messages.
func AdminFactorNames() []string {
	factors := EnrollableAdminFactors()
	names := make([]string, 0, len(factors))
	for _, f := range factors {
		names = append(names, f.String())
	}
	sort.Strings(names)
	return names
}
