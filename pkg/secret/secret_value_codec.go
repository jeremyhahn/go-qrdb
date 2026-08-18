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

package secret

import (
	"bytes"
	"encoding/json"
	"time"

	"gopkg.in/yaml.v3"
)

// A SecretValue appears in a configuration document as the reference string it
// resolves from — `password: "credential://db-key"` — never as the struct
// behind it. The codecs below make that shape work in both directions, so a
// config type can switch a field from string to SecretValue without its
// documents changing at all, and so a re-serialized config emits the reference
// again rather than the plaintext the reference resolved to.

// Compile-time proof that SecretValue speaks both configuration encodings in
// both directions. The marshallers take a value receiver so they apply to a
// SecretValue field as well as a *SecretValue one.
var (
	_ yaml.Unmarshaler = (*SecretValue)(nil)
	_ yaml.Marshaler   = SecretValue{}
	_ json.Unmarshaler = (*SecretValue)(nil)
	_ json.Marshaler   = SecretValue{}
)

// UnmarshalYAML decodes a scalar reference string into the value's Raw field,
// leaving it unresolved: ConfigProcessor.Process fills the enclave afterwards.
// A non-scalar node yields a SecretValueDecodeError. A null node (`password:`
// or `password: null`) never reaches here — yaml.v3 zeroes the destination
// without consulting the unmarshaler — so there is nothing for this method to
// reject; it leaves an empty target, which ConfigProcessor.Process then refuses
// with an UnresolvableTargetError. That is where the YAML null is caught, one
// layer up from where JSON catches its own null token.
func (v *SecretValue) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return &SecretValueDecodeError{Format: "yaml", Err: err}
	}
	v.setReference(raw)
	return nil
}

// MarshalYAML emits the reference the value resolves from, never the resolved
// plaintext, so a config round-trip cannot spill a secret into a document.
func (v SecretValue) MarshalYAML() (any, error) {
	return v.Raw, nil
}

// jsonNull is the one JSON token that decodes into a string without error.
// Accepting it would make a mistyped reference indistinguishable from a
// deliberately empty one, so UnmarshalJSON rejects it explicitly.
const jsonNull = "null"

// UnmarshalJSON decodes a JSON string reference into the value's Raw field,
// leaving it unresolved. Any other token — including null — yields a
// SecretValueDecodeError.
//
// Rejecting null departs from the usual convention that UnmarshalJSON treats it
// as a no-op, deliberately: `"password": null` in a security config is a
// mistake, and silently reading it as "no secret here" is exactly the quiet
// failure the protected target exists to prevent. It only ever reaches this
// method for a value-typed field — encoding/json sets a *SecretValue field to
// nil without calling the unmarshaler — so a consumer that genuinely wants an
// optional secret expresses it with a pointer field, where nil means absent.
func (v *SecretValue) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == jsonNull {
		return &SecretValueDecodeError{Format: "json", Err: errNullReference}
	}
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return &SecretValueDecodeError{Format: "json", Err: err}
	}
	v.setReference(raw)
	return nil
}

// MarshalJSON emits the reference the value resolves from, never the resolved
// plaintext.
func (v SecretValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.Raw)
}

// setReference points v at raw and drops everything a previous decode or
// resolution left behind, destroying the old enclave so a re-decoded field
// never keeps a stale secret alive in mlocked memory.
func (v *SecretValue) setReference(raw string) {
	v.Clear()
	v.Raw = raw
	v.Strategy = ""
	v.ExpiresAt = time.Time{}
}
