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

package secure_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	securesdk "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// The live empty values, liveness, the scoped Secret borrow and the
// constant-time comparisons must all be reachable from the SDK alone.

func TestNewEmptyBuffer_ThroughTheSDK(t *testing.T) {
	b := securesdk.NewEmptyBuffer()
	if !b.IsAlive() || b.Size() != 0 {
		t.Fatalf("NewEmptyBuffer: alive %v size %d, want a live empty Buffer", b.IsAlive(), b.Size())
	}
	if eq, err := b.EqualBytes(nil); err != nil || !eq {
		t.Fatalf("EqualBytes(nil) = (%v, %v), want (true, nil)", eq, err)
	}
	b.Destroy()
	if b.IsAlive() {
		t.Fatal("IsAlive() = true after Destroy")
	}
	var nbe *securesdk.NilBufferError
	if _, err := b.EqualBytes(nil); !errors.As(err, &nbe) {
		t.Fatalf("EqualBytes after Destroy: err = %v, want *NilBufferError", err)
	}
}

func TestNewEmptySecret_ThroughTheSDK(t *testing.T) {
	ctx := context.Background()
	s := securesdk.NewEmptySecret()
	if !s.IsAlive() {
		t.Fatal("IsAlive() = false for a fresh empty Secret")
	}
	if err := s.WithBytes(ctx, func(v []byte) error {
		if len(v) != 0 {
			t.Fatalf("view length = %d, want 0", len(v))
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	s.Destroy()
	var nse *securesdk.NilSecretError
	if err := s.WithBytes(ctx, func([]byte) error { return nil }); !errors.As(err, &nse) {
		t.Fatalf("WithBytes after Destroy: err = %v, want *NilSecretError", err)
	}
}

func TestBufferEqual_ThroughTheSDK(t *testing.T) {
	a, err := securesdk.NewBufferFromBytes([]byte("material"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer a.Destroy()
	b, err := securesdk.NewBufferFromBytes([]byte("material"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer b.Destroy()
	c, err := securesdk.NewBufferFromBytes([]byte("Material"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer c.Destroy()

	if eq, err := a.Equal(b); err != nil || !eq {
		t.Fatalf("Equal(same bytes) = (%v, %v), want (true, nil)", eq, err)
	}
	if eq, err := a.Equal(c); err != nil || eq {
		t.Fatalf("Equal(different bytes) = (%v, %v), want (false, nil)", eq, err)
	}
	if eq, err := a.EqualBytes([]byte("material")); err != nil || !eq {
		t.Fatalf("EqualBytes = (%v, %v), want (true, nil)", eq, err)
	}
}

func TestSecretWithBytes_ThroughTheSDK(t *testing.T) {
	ctx := context.Background()
	s, err := securesdk.NewSecret(ctx, []byte("material"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer s.Destroy()
	sentinel := errors.New("fn failed")
	if err := s.WithBytes(ctx, func(v []byte) error {
		if !bytes.Equal(v, []byte("material")) {
			t.Fatal("WithBytes did not hand fn the sealed plaintext")
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the callback's error", err)
	}
	if !s.IsAlive() {
		t.Fatal("WithBytes destroyed the Secret")
	}
}
