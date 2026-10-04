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

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOnBehalf_RoundTrips(t *testing.T) {
	for name, o := range map[string]OnBehalf{
		"a request":  {Method: "kv.Put", Assertion: "YXNzZXJ0aW9u", Signature: "c2ln", Payload: []byte{0x0a, 0x01, 'k'}},
		"no payload": {Method: "admin.Health", Assertion: "a", Signature: "s", Payload: []byte{}},
	} {
		b, err := EncodeOnBehalf(o)
		if err != nil {
			t.Fatalf("%s: encode: %v", name, err)
		}
		got, err := DecodeOnBehalf(b)
		if err != nil || !reflect.DeepEqual(got, o) {
			t.Fatalf("%s: round trip = %+v, %v; want %+v", name, got, err, o)
		}
	}
}

func TestOnBehalf_RefusesMalformedEnvelopes(t *testing.T) {
	good, err := EncodeOnBehalf(OnBehalf{Method: "kv.Put", Assertion: "a", Signature: "s"})
	if err != nil {
		t.Fatal(err)
	}
	emptyMethod, _ := EncodeOnBehalf(OnBehalf{Assertion: "a", Signature: "s"})
	for name, b := range map[string][]byte{
		"empty":               nil,
		"truncated length":    good[:1],
		"truncated method":    good[:4],
		"truncated assertion": good[:2+6+1],
		"truncated signature": good[:len(good)-1],
		"no method":           emptyMethod,
	} {
		var fe *OnBehalfFieldError
		if _, err := DecodeOnBehalf(b); !errors.As(err, &fe) || fe.Error() == "" {
			t.Errorf("%s: DecodeOnBehalf = %v; want *OnBehalfFieldError", name, err)
		}
	}
	var tooLong *MethodTooLongError
	if _, err := EncodeOnBehalf(OnBehalf{Method: strings.Repeat("m", MaxMethodLen+1)}); !errors.As(err, &tooLong) {
		t.Errorf("an over-long method encoded: %v", err)
	}
	var fe *OnBehalfFieldError
	if _, err := EncodeOnBehalf(OnBehalf{Method: "m", Assertion: strings.Repeat("a", 1<<16)}); !errors.As(err, &fe) {
		t.Errorf("an over-long assertion encoded: %v", err)
	}
}
