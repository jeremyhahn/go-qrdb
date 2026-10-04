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

package main

import (
	"encoding/json"
	"os"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// officersFileEntry is one holder in an --officers-file document. Unlike
// --officers/--custodians (username=path/to/csr.pem, Security Officer or
// custodian only, certificate key only), it carries the full holder shape
// `qrdb init` accepts (go-qrdb#4578 review item 1): a separate
// share-envelope key, additional authentication factors, and the
// designated factor -- everything OfficerRegisterRequest/
// BarrierOfficerRegister takes, because `qrdb init` is the ONLY place
// Shamir custody over the system barrier is ever established and a holder
// who needs any of those has no other registration path to get them
// through.
//
// CSRPEMPath and EncryptionKeyPath name files on disk, read at parse time,
// exactly like --admin-csr/--admin-cert/--officers already do: the CLI
// never accepts key material inline on the command line or embedded in
// the document itself.
type officersFileEntry struct {
	// Username is the holder's unique identifier.
	Username string `json:"username"`

	// Role is "so" (the default, empty) or "custodian".
	Role string `json:"role,omitempty"`

	// CSRPEMPath is the path to the holder's PEM certificate signing
	// request. Required.
	CSRPEMPath string `json:"csr_pem"`

	// EncryptionKEM names the HPKE suite of EncryptionKeyPath's key:
	// dhkem-p256, dhkem-p384, dhkem-x25519 or mlkem768-x25519. Required
	// when EncryptionKeyPath is set; omit both to have the certificate key
	// receive the share envelope instead.
	EncryptionKEM string `json:"encryption_kem,omitempty"`

	// EncryptionKeyPath is the path to the holder's separate share-envelope
	// public key, raw bytes in EncryptionKEM's own encoding (not PEM, not
	// base64 -- exactly the bytes keydelivery.Suite.PublicKey.Bytes wrote).
	EncryptionKeyPath string `json:"encryption_key,omitempty"`

	// Factors are additional authentication factors to enrol, inline:
	// each factor's own enrolment parameters (a FIDO2 attestation object,
	// a PIV slot certificate, ...) are themselves JSON, produced by that
	// factor's own enrolment tooling, not file references. "mtls" is
	// always enrolled, from the issued certificate.
	Factors []transport.HolderFactorEnrolment `json:"factors,omitempty"`

	// Designated names the factor fixed at enrolment under the designated
	// factor policy; "mtls" when empty.
	Designated string `json:"designated,omitempty"`
}

// officersFileDocument is the top-level shape of an --officers-file
// document.
type officersFileDocument struct {
	Officers []officersFileEntry `json:"officers"`
}

// parseOfficersFile reads path as an officersFileDocument and resolves each
// entry's CSRPEMPath/EncryptionKeyPath into the ceremony's wire holder
// shape. An empty Officers list is refused: an --officers-file with
// nothing in it is almost certainly a mistake, not an intentional
// single-admin request (which omits --officers-file entirely).
func parseOfficersFile(path string) ([]transport.CeremonyOfficerRequest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &FileReadError{Path: path, Err: err}
	}
	var doc officersFileDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, &InvalidArgError{Arg: "--officers-file", Reason: "not valid JSON: " + err.Error()}
	}
	if len(doc.Officers) == 0 {
		return nil, &InvalidArgError{Arg: "--officers-file", Reason: "names no officers"}
	}
	officers := make([]transport.CeremonyOfficerRequest, 0, len(doc.Officers))
	for _, e := range doc.Officers {
		if e.Username == "" {
			return nil, &InvalidArgError{Arg: "--officers-file", Reason: "an officer is missing username"}
		}
		if e.CSRPEMPath == "" {
			return nil, &InvalidArgError{Arg: "--officers-file", Reason: "officer " + e.Username + " is missing csr_pem"}
		}
		csrPEM, err := os.ReadFile(e.CSRPEMPath)
		if err != nil {
			return nil, &FileReadError{Path: e.CSRPEMPath, Err: err}
		}
		req := transport.CeremonyOfficerRequest{
			Username:      e.Username,
			Role:          e.Role,
			CSRPEM:        string(csrPEM),
			EncryptionKEM: e.EncryptionKEM,
			Factors:       e.Factors,
			Designated:    e.Designated,
		}
		if e.EncryptionKeyPath != "" {
			encKey, err := os.ReadFile(e.EncryptionKeyPath)
			if err != nil {
				return nil, &FileReadError{Path: e.EncryptionKeyPath, Err: err}
			}
			req.EncryptionKey = encKey
		}
		officers = append(officers, req)
	}
	return officers, nil
}
