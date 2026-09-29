// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
//  1. GNU Affero General Public License v3.0 (AGPL-3.0)
//
//  2. Commercial License
//     Contact licensing@automatethethings.com for commercial licensing options.

package ceremony

import (
	"sort"
	"strings"
)

// EnrollmentKind names how an operator supplies the key material that an
// identity will authenticate with.
//
// The three kinds serve the platform administrator: does this person want a
// certificate issued to them, or do they already hold one? A share holder
// (Security Officer or key custodian) is always enrolled from a CSR, because
// the ceremony CA must assign the holder's certificate identity; the holder
// enrolment APIs take a CSR and nothing else.
type EnrollmentKind string

const (
	// EnrollmentKindCSR supplies a PEM certificate signing request. The CA
	// signs it and the private key never leaves the operator.
	EnrollmentKindCSR EnrollmentKind = "csr"

	// EnrollmentKindCert supplies a PEM certificate the operator already
	// holds -- typically one already resident on a PIV card or a security
	// key. Nothing is issued; the certificate is validated against the
	// configured trust anchors and adopted.
	EnrollmentKindCert EnrollmentKind = "cert"

	// EnrollmentKindPubKey supplies a bare PEM public key. No chain can be
	// built from one, so trust is asserted by the administrator running the
	// ceremony rather than derived. It exists for administrators whose token
	// cannot export a certificate.
	EnrollmentKindPubKey EnrollmentKind = "pubkey"
)

// enrollmentKinds is the set of kinds an operator may name, used for both
// validation and for listing what is accepted in an error message.
var enrollmentKinds = map[EnrollmentKind]struct{}{
	EnrollmentKindCSR:    {},
	EnrollmentKindCert:   {},
	EnrollmentKindPubKey: {},
}

// EnrollmentSpec is a parsed operator-supplied enrollment argument.
type EnrollmentSpec struct {
	// Username identifies the person being enrolled.
	Username string

	// Kind selects how Path is interpreted.
	Kind EnrollmentKind

	// Path is the file holding the CSR, certificate, or public key.
	Path string
}

// EnrollmentKindNames returns the accepted kind names in a stable order, for
// flag help and error messages.
func EnrollmentKindNames() []string {
	names := make([]string, 0, len(enrollmentKinds))
	for kind := range enrollmentKinds {
		names = append(names, string(kind))
	}
	sort.Strings(names)
	return names
}

// ParseEnrollmentSpec turns one operator-supplied "<username>:<kind>:<path>"
// argument into a spec.
//
// It lives here, not in each binary's CLI, for the same reason
// ParseAdminFactors does: `--so alice:cert:/path/a.pem` has to mean the same
// thing to qrdbd, xkmsd and quicbaasd. Three binaries with three parsers is how
// this concept grew two vocabularies in the first place -- quicbaasd already
// accepted three kinds while xkmsd accepted a bare path and nothing else.
//
// The two-field form "<username>:<path>" is accepted and means csr. That is
// the shape xkmsd has always documented, so rejecting it would break every
// existing invocation to gain nothing; a kind is simply optional, and its
// absence means the kind that was previously the only possibility.
//
// A path may itself contain colons, so the kind is recognised by matching the
// second field against the known set rather than by counting separators. A
// second field that is not a kind is therefore treated as the start of the
// path, which is what makes "alice:/etc/ssl:certs/a.csr" parse the way an
// operator would expect.
func ParseEnrollmentSpec(arg string) (EnrollmentSpec, error) {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" {
		return EnrollmentSpec{}, &InvalidEnrollmentSpecError{Arg: arg, Reason: "empty"}
	}

	username, remainder, found := strings.Cut(trimmed, ":")
	username = strings.TrimSpace(username)
	if !found || username == "" || remainder == "" {
		return EnrollmentSpec{}, &InvalidEnrollmentSpecError{
			Arg:    arg,
			Reason: "expected <username>:<kind>:<path> or <username>:<path>",
		}
	}

	kind := EnrollmentKindCSR
	path := remainder
	if candidate, rest, hasKind := strings.Cut(remainder, ":"); hasKind {
		normalized := EnrollmentKind(strings.ToLower(strings.TrimSpace(candidate)))
		if _, known := enrollmentKinds[normalized]; known {
			kind = normalized
			path = rest
		}
	}

	path = strings.TrimSpace(path)
	if path == "" {
		return EnrollmentSpec{}, &InvalidEnrollmentSpecError{Arg: arg, Reason: "no path"}
	}

	return EnrollmentSpec{Username: username, Kind: kind, Path: path}, nil
}

// ParseEnrollmentSpecs parses repeated arguments and rejects a duplicated
// username.
//
// Two entries for one person are always a mistake -- either the operator meant
// two different people, or they meant one and supplied conflicting material --
// and silently keeping the last would enroll an identity the operator did not
// choose.
func ParseEnrollmentSpecs(args []string) ([]EnrollmentSpec, error) {
	specs := make([]EnrollmentSpec, 0, len(args))
	seen := make(map[string]struct{}, len(args))

	for _, arg := range args {
		spec, err := ParseEnrollmentSpec(arg)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[spec.Username]; duplicate {
			return nil, &DuplicateEnrollmentUsernameError{Username: spec.Username}
		}
		seen[spec.Username] = struct{}{}
		specs = append(specs, spec)
	}

	return specs, nil
}
