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

package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

// TestAuthenticatedCaller_Classification: an authenticated identity and a
// TLS-verified certificate select the authenticated counter; no identity, an
// identity the sealed window admitted anonymously, and an unverified
// certificate select the anonymous one.
func TestAuthenticatedCaller_Classification(t *testing.T) {
	id := &Identity{Subject: "officer", TenantID: "system"}
	cases := map[string]struct {
		ctx  context.Context
		auth bool
	}{
		"no caller":           {context.Background(), false},
		"identity":            {WithIdentity(context.Background(), id), true},
		"sealed-window anon":  {sealedwindow.WithAnonymous(WithIdentity(context.Background(), id)), false},
		"verified TLS":        {WithTLSState(context.Background(), &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{}}}), true},
		"unverified TLS cert": {WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}), false},
	}
	for name, c := range cases {
		if got := AuthenticatedCaller(c.ctx); got != c.auth {
			t.Errorf("%s: authenticated = %v, want %v", name, got, c.auth)
		}
	}
}

// TestSOPINBackoff_Caps: the backoff stops doubling at SOPINMaxBackoff and
// does not overflow however long the failures go on.
func TestSOPINBackoff_Caps(t *testing.T) {
	cases := map[int]time.Duration{0: SOPINBaseBackoff, 2: 4 * SOPINBaseBackoff, 20: SOPINMaxBackoff, 1_000_000: SOPINMaxBackoff}
	for n, want := range cases {
		if got := soPINBackoff(n); got != want {
			t.Errorf("soPINBackoff(%d) = %v, want %v", n, got, want)
		}
	}
}

// errWrong is a wrong-PIN answer for the lockout tests.
var errWrong = errors.New("wrong")

// TestSOPINLockout_KeysAndClassesAreSeparate: each key has its own pair of
// counters, and within a key the anonymous and authenticated classes count
// separately; a correct PIN resets only its own counter and an untouched key
// is forgotten.
func TestSOPINLockout_KeysAndClassesAreSeparate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewSOPINLockout(func() time.Time { return now })
	wrong := func() error { return errWrong }
	right := func() error { return nil }
	isWrong := func(err error) bool { return errors.Is(err, errWrong) }
	for i := 0; i < SOPINFreeFailures; i++ {
		if err := l.Check("acme", false, wrong, isWrong); !errors.Is(err, errWrong) {
			t.Fatalf("failure %d = %v", i, err)
		}
	}
	var locked *SOPINLockedError
	if err := l.Check("acme", false, right, isWrong); !errors.As(err, &locked) || !errors.Is(err, ErrSOPINLocked) || locked.RetryAfter != SOPINBaseBackoff {
		t.Fatalf("anonymous after the free failures = %v", err)
	}
	if err := l.Check("acme", true, right, isWrong); err != nil {
		t.Fatalf("authenticated on the same key = %v", err)
	}
	if err := l.Check("globex", false, right, isWrong); err != nil {
		t.Fatalf("another key = %v", err)
	}
	if f, until := l.Status("acme", false); f != SOPINFreeFailures || until.IsZero() {
		t.Fatalf("status = %d, %v", f, until)
	}
	if f, until := l.Status("nobody", true); f != 0 || !until.IsZero() {
		t.Fatalf("unknown key status = %d, %v", f, until)
	}
	errOther := errors.New("store down")
	if err := l.Check("acme", true, func() error { return errOther }, isWrong); !errors.Is(err, errOther) {
		t.Fatalf("a non-mismatch error = %v", err)
	}
	if (&SOPINLockedError{RetryAfter: time.Second}).Error() == "" || !l.Now().Equal(now) {
		t.Fatal("error message or clock")
	}
}
