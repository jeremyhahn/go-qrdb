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

package cluster

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"strconv"
	"strings"
)

// memberIdentityPrefix is the scheme tag go-quicraft puts in front of a node
// identity. It is part of the value: go-quicraft compares identities as opaque
// strings, so an identity without it never matches the one a peer presents.
const memberIdentityPrefix = "spki:"

// memberIdentityHexLen is the length of the hex-encoded SHA-256 digest that
// follows memberIdentityPrefix.
const memberIdentityHexLen = sha256.Size * 2

// MemberIdentityFromCertificate returns the Raft member identity a node
// presents when it holds leaf as its transport certificate: "spki:" followed by
// the hex SHA-256 of the certificate's SubjectPublicKeyInfo.
//
// It is the value go-quicraft's Host.NodeIdentity reports and the value its
// receive gate compares against Config.MemberIdentities. Because it hashes the
// public key rather than the certificate, it is stable across a certificate
// renewal on the same keypair and changes only when the keypair does. It is a
// hash of a public key and is safe to log, print and copy.
//
// A nil certificate yields "".
func MemberIdentityFromCertificate(leaf *x509.Certificate) string {
	if leaf == nil {
		return ""
	}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return memberIdentityPrefix + hex.EncodeToString(sum[:])
}

// MemberIdentityFromPEM returns the Raft member identity of the first
// CERTIFICATE block in certPEM — the leaf of a node certificate file or chain.
// See MemberIdentityFromCertificate.
func MemberIdentityFromPEM(certPEM []byte) (string, error) {
	rest := certPEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return "", &MemberIdentityCertificateError{Reason: "no CERTIFICATE block"}
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", &MemberIdentityCertificateError{Reason: "parse certificate", Err: err}
		}
		return MemberIdentityFromCertificate(leaf), nil
	}
}

// ValidateMemberIdentity reports whether s has the form go-quicraft produces:
// "spki:" followed by 64 lower-case hex digits. A value of any other shape can
// never match what a peer presents, so it is refused rather than accepted and
// left to drop every message from the member it names.
func ValidateMemberIdentity(s string) error {
	digest, ok := strings.CutPrefix(s, memberIdentityPrefix)
	if !ok || len(digest) != memberIdentityHexLen {
		return &MemberIdentityFormatError{Identity: s}
	}
	for i := 0; i < len(digest); i++ {
		c := digest[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return &MemberIdentityFormatError{Identity: s}
		}
	}
	return nil
}

// ParseMemberIdentities parses "replicaID=identity" specifications — the
// member_identities configuration and the --member-identities flag — into the
// map go-quicraft takes as Config.MemberIdentities. Blank specifications are
// skipped so a trailing comma in a comma-separated value is harmless. Every
// identity must pass ValidateMemberIdentity, replica ID 0 is refused, and no
// replica may be named twice.
func ParseMemberIdentities(specs []string) (map[uint64]string, error) {
	out := make(map[uint64]string, len(specs))
	for _, raw := range specs {
		spec := strings.TrimSpace(raw)
		if spec == "" {
			continue
		}
		idPart, identity, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, &MemberIdentityParseError{Input: spec}
		}
		id, err := strconv.ParseUint(strings.TrimSpace(idPart), 10, 64)
		if err != nil || id == 0 {
			return nil, &MemberIdentityParseError{Input: spec}
		}
		identity = strings.TrimSpace(identity)
		if err := ValidateMemberIdentity(identity); err != nil {
			return nil, err
		}
		if _, dup := out[id]; dup {
			return nil, &MemberIdentityParseError{Input: spec}
		}
		out[id] = identity
	}
	return out, nil
}

// ResolveMemberIdentities builds the Config.MemberIdentities map for a replica
// that runs a Raft transport: the operator-supplied specs (see
// ParseMemberIdentities) plus this replica's OWN identity, derived from the
// certificate it presents (selfCertPEM).
//
// The local entry is derived rather than required because the node already
// holds the only input it needs; a peer's identity cannot be derived here,
// because learning it from the peer's own connection would let any holder of
// a cluster certificate claim any replica ID — the attack identities exist to
// close. A configured local entry that disagrees with the certificate is
// refused (*MemberIdentityMismatchError): go-quicraft would refuse it too, but
// only after the host is built, and naming the configuration key here is the
// actionable form.
//
// Whether the result covers every founding member, or names an anchor for a
// joiner, is go-quicraft's rule and is left to StartShard to enforce.
func ResolveMemberIdentities(specs []string, selfID uint64, selfCertPEM []byte) (map[uint64]string, error) {
	identities, err := ParseMemberIdentities(specs)
	if err != nil {
		return nil, err
	}
	if len(selfCertPEM) == 0 {
		return identities, nil
	}
	self, err := MemberIdentityFromPEM(selfCertPEM)
	if err != nil {
		return nil, err
	}
	if configured, ok := identities[selfID]; ok && configured != self {
		return nil, &MemberIdentityMismatchError{ReplicaID: selfID, Configured: configured, Derived: self}
	}
	identities[selfID] = self
	return identities, nil
}
