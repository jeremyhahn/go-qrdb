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

package transport

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

func TestBufferFromWire_MovesValueIntoProtectedMemory(t *testing.T) {
	wire := []byte("wire-secret")

	buf, err := BufferFromWire(wire)
	if err != nil {
		t.Fatalf("BufferFromWire: %v", err)
	}
	defer buf.Destroy()

	if err := buf.WithBytes(func(b []byte) error {
		if string(b) != "wire-secret" {
			t.Errorf("buffer holds %q, want wire-secret", b)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
}

func TestBufferFromWire_WipesTheDecodedSlice(t *testing.T) {
	wire := []byte("wire-secret")

	buf, err := BufferFromWire(wire)
	if err != nil {
		t.Fatalf("BufferFromWire: %v", err)
	}
	defer buf.Destroy()

	for i, b := range wire {
		if b != 0 {
			t.Fatalf("decoded slice byte %d = %#x, want it wiped", i, b)
		}
	}
}

func TestBufferFromWire_RejectsEmptyValue(t *testing.T) {
	for name, wire := range map[string][]byte{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			buf, err := BufferFromWire(wire)
			var size *secure.InvalidSizeError
			if !errors.As(err, &size) {
				t.Fatalf("err = %T %v, want *secure.InvalidSizeError", err, err)
			}
			if buf != nil {
				t.Error("an empty wire value returned a buffer")
			}
		})
	}
}

func bufferString(t *testing.T, buf *secure.Buffer) string {
	t.Helper()
	var out string
	if err := buf.WithBytes(func(b []byte) error {
		out = string(b)
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	return out
}

func TestBufferFromBase64_DecodesIntoBufferForEveryPaddingShape(t *testing.T) {
	for _, want := range []string{"a", "ab", "abc", "abcd", "\x00\xff\x10binary"} {
		src := []byte(base64.StdEncoding.EncodeToString([]byte(want)))
		buf, err := BufferFromBase64(src)
		if err != nil {
			t.Fatalf("BufferFromBase64(%q): %v", want, err)
		}
		if got := bufferString(t, buf); got != want {
			t.Errorf("decoded %q, want %q", got, want)
		}
		buf.Destroy()
	}
}

func TestBufferFromBase64_WipesTheEncodedSource(t *testing.T) {
	src := []byte(base64.StdEncoding.EncodeToString([]byte("wire-secret")))
	buf, err := BufferFromBase64(src)
	if err != nil {
		t.Fatalf("BufferFromBase64: %v", err)
	}
	defer buf.Destroy()
	for i, b := range src {
		if b != 0 {
			t.Fatalf("source byte %d = %#x, want it wiped", i, b)
		}
	}
}

func TestBufferFromBase64_FailsClosed(t *testing.T) {
	for name, src := range map[string]string{"bad length": "abc", "bad alphabet": "ab!d"} {
		t.Run(name, func(t *testing.T) {
			buf, err := BufferFromBase64([]byte(src))
			if err == nil || buf != nil {
				t.Fatalf("buf, err = %v, %v; want nil buffer and an error", buf, err)
			}
		})
	}
	_, err := BufferFromBase64(nil)
	var size *secure.InvalidSizeError
	if !errors.As(err, &size) {
		t.Fatalf("empty err = %T %v, want *secure.InvalidSizeError", err, err)
	}
}

func TestWireValue_DecodesJSONFieldIntoBuffer(t *testing.T) {
	var resp struct {
		Value WireValue `json:"value"`
	}
	body := `{"value":"` + base64.StdEncoding.EncodeToString([]byte("json-secret")) + `"}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	defer resp.Value.Buffer.Destroy()
	if got := bufferString(t, resp.Value.Buffer); got != "json-secret" {
		t.Errorf("decoded %q, want json-secret", got)
	}
}

func TestWireValue_RejectsNonStringAndEmpty(t *testing.T) {
	for name, body := range map[string]string{
		"number":  `{"value":12}`,
		"null":    `{"value":null}`,
		"empty":   `{"value":""}`,
		"corrupt": `{"value":"ab!d"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var resp struct {
				Value WireValue `json:"value"`
			}
			if err := json.Unmarshal([]byte(body), &resp); err == nil {
				resp.Value.Buffer.Destroy()
				t.Fatal("expected an error")
			}
			if resp.Value.Buffer != nil {
				t.Error("a rejected value left a buffer")
			}
		})
	}
}

func TestWireValueError_Message(t *testing.T) {
	if (&WireValueError{}).Error() == "" {
		t.Error("empty message")
	}
}
