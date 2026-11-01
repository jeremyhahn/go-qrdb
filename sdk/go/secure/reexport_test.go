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

package secure_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	securesdk "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

func TestNewSecretFromBuffer_SealsAndConsumesTheBufferThroughTheSDK(t *testing.T) {
	ctx := context.Background()
	const material = "sealed-through-the-sdk"
	buf, err := securesdk.NewBufferFromBytes([]byte(material))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	s, err := securesdk.NewSecretFromBuffer(ctx, buf)
	if err != nil {
		t.Fatalf("NewSecretFromBuffer: %v", err)
	}
	defer s.Destroy()

	// The buffer is consumed: its plaintext now lives only in the Secret.
	var nilBuf *securesdk.NilBufferError
	if _, err := securesdk.CopyOut(buf); !errors.As(err, &nilBuf) {
		t.Fatalf("CopyOut on the consumed buffer: err = %v, want *NilBufferError", err)
	}
	if err := s.WithBytes(ctx, func(b []byte) error {
		if string(b) != material {
			t.Errorf("Secret holds %q, want %q", b, material)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
}

func TestNewSecretFromBuffer_RefusesNilAndDestroyedThroughTheSDK(t *testing.T) {
	ctx := context.Background()
	var nilBuf *securesdk.NilBufferError
	if _, err := securesdk.NewSecretFromBuffer(ctx, nil); !errors.As(err, &nilBuf) {
		t.Fatalf("NewSecretFromBuffer(nil): err = %v, want *NilBufferError", err)
	}
	spent, err := securesdk.NewBuffer(8)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	spent.Destroy()
	if _, err := securesdk.NewSecretFromBuffer(ctx, spent); !errors.As(err, &nilBuf) {
		t.Fatalf("NewSecretFromBuffer(destroyed): err = %v, want *NilBufferError", err)
	}
}

func TestWipe_ZeroesThroughTheSDK(t *testing.T) {
	b := []byte("not for long")
	securesdk.Wipe(b)
	if !bytes.Equal(b, make([]byte, len(b))) {
		t.Fatalf("Wipe left %q", b)
	}
	securesdk.Wipe(nil) // a nil slice is a no-op, not a panic
}

func TestProviders_SatisfyPassphraseProviderThroughTheSDK(t *testing.T) {
	var _ securesdk.PassphraseProvider = securesdk.NewTerminalProvider()
	var _ securesdk.PassphraseProvider = securesdk.NewStdinProvider()
	if securesdk.NewTerminalProvider() == nil || securesdk.NewStdinProvider() == nil {
		t.Fatal("a provider constructor returned nil")
	}
}

func TestNewCallbackProvider_ReturnsTheCallbackPassphraseThroughTheSDK(t *testing.T) {
	var gotPrompt string
	p := securesdk.NewCallbackProvider(func(_ context.Context, prompt string) ([]byte, error) {
		gotPrompt = prompt
		return []byte("hunter2"), nil
	})
	buf, err := p.Passphrase(context.Background(), "unseal: ")
	if err != nil {
		t.Fatalf("Passphrase: %v", err)
	}
	defer buf.Destroy()
	if gotPrompt != "unseal: " {
		t.Errorf("callback saw prompt %q", gotPrompt)
	}
	out, err := securesdk.CopyOut(buf)
	if err != nil || string(out) != "hunter2" {
		t.Fatalf("Passphrase buffer = %q, %v", out, err)
	}
}

func TestNewCallbackProvider_SurfacesTheCallbackErrorThroughTheSDK(t *testing.T) {
	cause := errors.New("no operator")
	p := securesdk.NewCallbackProvider(func(context.Context, string) ([]byte, error) {
		return nil, cause
	})
	if _, err := p.Passphrase(context.Background(), "p"); !errors.Is(err, cause) {
		t.Fatalf("Passphrase: err = %v, want the callback's error", err)
	}
}

// TestProcessAllocator_IsOneProcessWideBudgetThroughTheSDK: the lazily
// installed default is the same allocator on every call, and once it exists
// SetAllocator is refused, as the binary-only contract says.
func TestProcessAllocator_IsOneProcessWideBudgetThroughTheSDK(t *testing.T) {
	a, err := securesdk.ProcessAllocator()
	if err != nil || a == nil {
		t.Fatalf("ProcessAllocator: %v, %v", a, err)
	}
	b, err := securesdk.ProcessAllocator()
	if err != nil || b != a {
		t.Fatalf("second ProcessAllocator = %p, %v; want %p", b, err, a)
	}
	if err := securesdk.SetAllocator(a); !errors.Is(err, securesdk.ErrAllocatorInstalled) {
		t.Fatalf("SetAllocator after install: err = %v, want ErrAllocatorInstalled", err)
	}
}

// wipeAllChildEnv marks the re-executed test binary that runs the WipeAll
// body. WipeAll destroys the process Sealer, after which no Secret in that
// process opens again, so it cannot share a process with the other tests.
const wipeAllChildEnv = "QRDB_SDK_SECURE_WIPEALL_CHILD"

func TestWipeAll_DestroysLiveEnclavesThroughTheSDK(t *testing.T) {
	if os.Getenv(wipeAllChildEnv) == "1" {
		wipeAllChild(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and every other argument is a compile-time literal. No shell is involved and nothing from outside the process reaches this argv.
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWipeAll_DestroysLiveEnclavesThroughTheSDK$", "-test.count=1")
	cmd.Env = append(os.Environ(), wipeAllChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("WipeAll child failed: %v\n%s", err, out)
	}
}

func wipeAllChild(t *testing.T) {
	ctx := context.Background()
	buf, err := securesdk.NewBufferFromBytes([]byte("live-buffer"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	s, err := securesdk.NewSecret(ctx, []byte("live-secret"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}

	n, err := securesdk.WipeAll(ctx)
	if err != nil {
		t.Fatalf("WipeAll: %v", err)
	}
	// The count is how many enclaves were still held at return: nothing
	// here holds a borrow, so the sweep reclaims every one.
	if n != 0 {
		t.Fatalf("WipeAll left %d enclaves held, want 0", n)
	}
	var nilBuf *securesdk.NilBufferError
	if _, err := securesdk.CopyOut(buf); !errors.As(err, &nilBuf) {
		t.Fatalf("CopyOut after WipeAll: err = %v, want *NilBufferError", err)
	}
	if err := s.WithBytes(ctx, func([]byte) error { return nil }); !errors.Is(err, securesdk.ErrSealerWiped) {
		t.Fatalf("Secret after WipeAll: err = %v, want ErrSealerWiped", err)
	}
}
