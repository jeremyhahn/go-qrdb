// Copyright 2026 Jeremy Hahn
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

package transport

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestSecret_JSONMatchesTheStringForm: a Secret field encodes exactly as the
// string field it replaced, so the wire format is unchanged.
func TestSecret_JSONMatchesTheStringForm(t *testing.T) {
	type stringForm struct {
		SOPin   string `json:"so_pin,omitempty"`
		UserPin string `json:"user_pin"`
	}
	type secretForm struct {
		SOPin   Secret `json:"so_pin,omitempty"`
		UserPin Secret `json:"user_pin"`
	}
	for _, pin := range []string{"", "1234", "p\"in\\<&>é\n", "\xff"} {
		want, err := json.Marshal(stringForm{SOPin: pin, UserPin: pin})
		if err != nil {
			t.Fatalf("marshal string form: %v", err)
		}
		got, err := json.Marshal(secretForm{SOPin: Secret(pin), UserPin: Secret(pin)})
		if err != nil {
			t.Fatalf("marshal secret form: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("pin %q: secret form %s, string form %s", pin, got, want)
		}

		var back secretForm
		if err := json.Unmarshal(want, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		var backString stringForm
		if err := json.Unmarshal(want, &backString); err != nil {
			t.Fatalf("unmarshal string form: %v", err)
		}
		if string(back.SOPin) != backString.SOPin || string(back.UserPin) != backString.UserPin {
			t.Fatalf("pin %q: decoded %q/%q, string form %q/%q", pin, back.SOPin, back.UserPin, backString.SOPin, backString.UserPin)
		}
	}
}

// TestSecret_NilEncodesAsEmptyString: a nil Secret on a field without
// omitempty encodes as "", as the empty string did.
func TestSecret_NilEncodesAsEmptyString(t *testing.T) {
	got, err := json.Marshal(struct {
		Pin Secret `json:"so_pin"`
	}{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != `{"so_pin":""}` {
		t.Fatalf("nil secret = %s", got)
	}
}

func TestSecret_UnmarshalNullAndNonString(t *testing.T) {
	var s Secret
	if err := json.Unmarshal([]byte(`null`), &s); err != nil || s != nil {
		t.Fatalf("null: %q, %v", s, err)
	}
	if err := json.Unmarshal([]byte(`1234`), &s); err == nil {
		t.Fatal("a JSON number decoded as a secret")
	}
}

func TestSecret_WipeZeroesInPlace(t *testing.T) {
	s := Secret("1234")
	alias := []byte(s)
	s.Wipe()
	if !bytes.Equal(alias, make([]byte, 4)) {
		t.Fatalf("Wipe left %q", alias)
	}
	Secret(nil).Wipe() // a nil secret wipes without panicking
}
