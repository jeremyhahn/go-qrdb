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

package protocol

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// OpIndexCreate key-scope prefix (go-qrdb#410)
// ---------------------------------------------------------------------------

func TestIndexCreateOpts_KeyPrefixRoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		keyPrefix string
	}{
		{name: "collection document prefix", keyPrefix: "__col:sales:users/"},
		{name: "entity type prefix", keyPrefix: "users/"},
		{name: "empty", keyPrefix: ""},
		{name: "binary bytes", keyPrefix: "\x00\xff/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := mustEncode(EncodeIndexCreateOpts("idx", "email", IndexCreateOptions{
				Unique:          true,
				Sparse:          true,
				Numeric:         true,
				CaseInsensitive: true,
				KeyPrefix:       tt.keyPrefix,
			}))
			name, field, opts, err := DecodeIndexCreateOpts(cmd[1:])
			if err != nil {
				t.Fatalf("DecodeIndexCreateOpts: %v", err)
			}
			if name != "idx" || field != "email" {
				t.Fatalf("name/field = %q/%q; want idx/email", name, field)
			}
			if opts.KeyPrefix != tt.keyPrefix {
				t.Errorf("KeyPrefix = %q; want %q", opts.KeyPrefix, tt.keyPrefix)
			}
			// The flags must survive alongside the new trailing block.
			if !opts.Unique || !opts.Sparse || !opts.Numeric || !opts.CaseInsensitive {
				t.Errorf("flags lost across the key-prefix extension: %+v", opts)
			}
		})
	}
}

// TestDecodeIndexCreateOpts_PreKeyPrefixPayload states the compatibility
// rule that makes this a safe format change: a command written before the
// key-scope prefix existed ends at the flag byte, and replaying it must yield
// an empty prefix rather than a decode error that would stall the apply loop.
func TestDecodeIndexCreateOpts_PreKeyPrefixPayload(t *testing.T) {
	full := mustEncode(EncodeIndexCreateOpts("idx", "email", IndexCreateOptions{Unique: true}))
	// Drop the trailing [prefixLen:2] block to reconstruct the old shape.
	old := full[1 : len(full)-2]

	name, field, opts, err := DecodeIndexCreateOpts(old)
	if err != nil {
		t.Fatalf("DecodeIndexCreateOpts(pre-#410 payload): %v", err)
	}
	if name != "idx" || field != "email" {
		t.Fatalf("name/field = %q/%q; want idx/email", name, field)
	}
	if !opts.Unique {
		t.Error("Unique flag lost decoding a pre-#410 payload")
	}
	if opts.KeyPrefix != "" {
		t.Errorf("KeyPrefix = %q; want empty for a pre-#410 payload", opts.KeyPrefix)
	}
}

func TestDecodeIndexCreateOpts_TruncatedKeyPrefix(t *testing.T) {
	full := mustEncode(EncodeIndexCreateOpts("idx", "email", IndexCreateOptions{KeyPrefix: "users/"}))

	tests := []struct {
		name    string
		payload []byte
		reason  string
	}{
		{
			name:    "one byte of the length",
			payload: full[1 : len(full)-len("users/")-1],
			reason:  "truncated key prefix length",
		},
		{
			name:    "length promises more than is present",
			payload: full[1 : len(full)-1],
			reason:  "key prefix length exceeds payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := DecodeIndexCreateOpts(tt.payload)
			if err == nil {
				t.Fatal("expected a decode error")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("error = %T; want *InvalidCommandError", err)
			}
			if !strings.Contains(ice.Error(), tt.reason) {
				t.Errorf("error = %q; want it to name %q", ice.Error(), tt.reason)
			}
		})
	}
}

func TestEncodeIndexCreateOpts_KeyPrefixTooLong(t *testing.T) {
	_, err := EncodeIndexCreateOpts("idx", "email", IndexCreateOptions{
		KeyPrefix: strings.Repeat("x", 1<<16),
	})
	if err == nil {
		t.Fatal("expected an error for a key prefix exceeding uint16")
	}
}
