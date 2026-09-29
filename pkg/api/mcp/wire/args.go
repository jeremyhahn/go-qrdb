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

package wire

import "encoding/json"

// Tool arguments are a flat JSON object. A request message maps onto one by
// its JSON field names: scalars stay scalars, []byte fields are base64
// strings (encoding/json's own rendering), and an array- or object-valued
// field travels as a JSON-encoded STRING — the convention every structured
// argument on this transport follows, because the tool schemas declare such
// arguments as strings. EncodeArgs and DecodeArgs are the one encoder and
// the one decoder for that mapping, shared by the server and the SDK client
// so the two cannot disagree about a field.

// HolderJSONFields are the structured arguments of the share-holder tools.
// Each is carried as a JSON-encoded string.
var HolderJSONFields = []string{"factors", "response_metadata", "method_data"}

// CeremonyJSONFields are the structured arguments of qrdb_ceremony_initialize:
// the initial holders and the TLS subject alternative names.
var CeremonyJSONFields = []string{"officers", "sans"}

// ArgsError is returned when a request cannot be mapped to or from tool
// arguments.
type ArgsError struct {
	Field string
	Err   error
}

// Error returns a human-readable description.
func (e *ArgsError) Error() string {
	if e.Field == "" {
		return "mcp: tool arguments: " + e.Err.Error()
	}
	return "mcp: tool argument " + e.Field + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ArgsError) Unwrap() error { return e.Err }

// EncodeArgs renders v, a request message, as tool arguments. Every field
// named in jsonFields that is present is re-encoded as a JSON string.
func EncodeArgs(v any, jsonFields ...string) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, &ArgsError{Err: err}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, &ArgsError{Err: err}
	}
	stringified := make(map[string]bool, len(jsonFields))
	for _, f := range jsonFields {
		stringified[f] = true
	}
	// Every other field travels as its JSON encoding, verbatim: a
	// json.RawMessage is emitted unchanged when the arguments are marshalled
	// onto the wire, and DecodeArgs re-reads it exactly.
	args := make(map[string]any, len(fields))
	for name, value := range fields {
		if stringified[name] {
			args[name] = string(value)
			continue
		}
		args[name] = value
	}
	return args, nil
}

// DecodeArgs maps tool arguments onto v, a request message. Every field
// named in jsonFields that arrives as a string is parsed as JSON first.
func DecodeArgs(args map[string]any, v any, jsonFields ...string) error {
	merged := make(map[string]any, len(args))
	for name, value := range args {
		merged[name] = value
	}
	for _, f := range jsonFields {
		s, ok := args[f].(string)
		if !ok {
			continue
		}
		if s == "" {
			delete(merged, f)
			continue
		}
		var parsed json.RawMessage
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			return &ArgsError{Field: f, Err: err}
		}
		merged[f] = parsed
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return &ArgsError{Err: err}
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &ArgsError{Err: err}
	}
	return nil
}
