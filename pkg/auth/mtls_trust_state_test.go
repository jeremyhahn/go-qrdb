package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
)

// trustStateVerifier is an IssuerVerifier whose VerifyPeerChain always fails
// with err; the other methods are never reached by ResolveVerifiedChains.
type trustStateVerifier struct{ err error }

var _ IssuerVerifier = (*trustStateVerifier)(nil)

func (v *trustStateVerifier) VerifyPeerChain(context.Context, *x509.Certificate, []*x509.Certificate) ([]*x509.Certificate, error) {
	return nil, v.err
}

func (v *trustStateVerifier) IsClusterCA(context.Context, *x509.Certificate) bool { return false }

func (v *trustStateVerifier) VerifyIssuer(context.Context, string, []*x509.Certificate) error {
	return v.err
}

// unreadRefusalError is a verifier refusal that says the trust state it judged
// against was incomplete, as pkg/clienttrust's PeerCertificateNotTrustedError
// does while a trust-source read fails.
type unreadRefusalError struct{ unread bool }

func (e *unreadRefusalError) Error() string               { return "certificate not trusted" }
func (e *unreadRefusalError) TrustStateUnavailable() bool { return e.unread }

func peerOnlyState() *tls.ConnectionState {
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
}

func TestResolveVerifiedChains_RefusalWhileTrustUnreadIsUnavailable(t *testing.T) {
	refusal := &unreadRefusalError{unread: true}
	_, err := ResolveVerifiedChains(context.Background(), peerOnlyState(), &trustStateVerifier{err: refusal})

	var unavailable *TrustStateUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want *TrustStateUnavailableError", err)
	}
	if !errors.Is(err, error(refusal)) {
		t.Fatalf("err = %v does not wrap the verifier's refusal", err)
	}
	if errors.Is(err, ErrInvalidCertificate) {
		t.Fatalf("err = %v is ErrInvalidCertificate; an unread trust state is not a denial", err)
	}
}

func TestResolveVerifiedChains_RefusalWithTrustReadIsInvalid(t *testing.T) {
	cases := map[string]error{
		"refusal with every source read": &unreadRefusalError{unread: false},
		"plain refusal":                  errors.New("x509: unknown authority"),
	}
	for name, refusal := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveVerifiedChains(context.Background(), peerOnlyState(), &trustStateVerifier{err: refusal})
			if !errors.Is(err, ErrInvalidCertificate) {
				t.Fatalf("err = %v, want ErrInvalidCertificate", err)
			}
		})
	}
}

func TestTrustStateUnavailable(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil":                    {nil, false},
		"plain error":            {errors.New("boom"), false},
		"refusal, sources read":  {&unreadRefusalError{unread: false}, false},
		"refusal, source unread": {&unreadRefusalError{unread: true}, true},
		"wrapped, source unread": {&TrustStateUnavailableError{Err: &unreadRefusalError{unread: true}}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := TrustStateUnavailable(tc.err); got != tc.want {
				t.Fatalf("TrustStateUnavailable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestTrustStateUnavailableError_Error(t *testing.T) {
	cases := map[string]struct {
		err  *TrustStateUnavailableError
		want string
	}{
		"bare":    {&TrustStateUnavailableError{}, "qrdb/auth: client trust state unavailable"},
		"wrapped": {&TrustStateUnavailableError{Err: errors.New("anchors unreadable")}, "qrdb/auth: client trust state unavailable: anchors unreadable"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}
