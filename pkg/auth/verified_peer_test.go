package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"
)

// poolChainVerifier verifies a peer chain against roots, as
// pkg/clienttrust.Pool.VerifyPeerChain does against its widened pool. It
// records each call so a test can tell the fallback ran.
type poolChainVerifier struct {
	roots *x509.CertPool
	calls int
}

var _ IssuerVerifier = (*poolChainVerifier)(nil)

func (v *poolChainVerifier) VerifyPeerChain(_ context.Context, leaf *x509.Certificate, intermediates []*x509.Certificate) ([]*x509.Certificate, error) {
	v.calls++
	pool := x509.NewCertPool()
	for _, c := range intermediates {
		pool.AddCert(c)
	}
	chains, err := leaf.Verify(x509.VerifyOptions{Roots: v.roots, Intermediates: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if err != nil {
		return nil, err
	}
	return chains[0], nil
}

func (v *poolChainVerifier) IsClusterCA(context.Context, *x509.Certificate) bool { return false }

func (v *poolChainVerifier) VerifyIssuer(context.Context, string, []*x509.Certificate) error {
	return nil
}

// issueTestCert returns a client certificate for cn signed by parent (self-
// signed when parent is nil), and its key for signing children.
func issueTestCert(t *testing.T, cn string, isCA bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func TestVerifiedPeerCertificate_NoTLSState(t *testing.T) {
	if _, err := VerifiedPeerCertificate(context.Background()); !errors.Is(err, ErrNoPeerCertificate) {
		t.Fatalf("err = %v, want ErrNoPeerCertificate", err)
	}
	ctx := WithTLSState(context.Background(), &tls.ConnectionState{})
	if _, err := VerifiedPeerCertificate(ctx); !errors.Is(err, ErrNoPeerCertificate) {
		t.Fatalf("err = %v, want ErrNoPeerCertificate", err)
	}
}

func TestVerifiedPeerCertificate_TLSVerifiedChain(t *testing.T) {
	ca, caKey := issueTestCert(t, "ca", true, nil, nil)
	leaf, _ := issueTestCert(t, "holder", false, ca, caKey)
	ctx := WithTLSState(context.Background(), &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf},
		VerifiedChains:   [][]*x509.Certificate{{leaf, ca}},
	})
	got, err := VerifiedPeerCertificate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != leaf {
		t.Fatal("leaf is not VerifiedChains[0][0]")
	}
}

func TestVerifiedPeerCertificate_UnverifiedWithoutVerifier(t *testing.T) {
	self, _ := issueTestCert(t, "mallory", false, nil, nil)
	ctx := WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{self}})
	if _, err := VerifiedPeerCertificate(ctx); !errors.Is(err, ErrInvalidCertificate) {
		t.Fatalf("err = %v, want ErrInvalidCertificate", err)
	}
}

func TestVerifiedPeerCertificate_VerifierTrustsChain(t *testing.T) {
	ca, caKey := issueTestCert(t, "ca", true, nil, nil)
	leaf, _ := issueTestCert(t, "holder", false, ca, caKey)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	v := &poolChainVerifier{roots: roots}
	ctx := WithPeerChainVerifier(WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}), v)
	got, err := VerifiedPeerCertificate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(leaf) || v.calls != 1 {
		t.Fatalf("leaf %v calls %d, want the presented leaf after one verification", got.Subject, v.calls)
	}
}

func TestVerifiedPeerCertificate_VerifierRefusesSelfSigned(t *testing.T) {
	ca, _ := issueTestCert(t, "ca", true, nil, nil)
	self, _ := issueTestCert(t, "mallory", false, nil, nil)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	v := &poolChainVerifier{roots: roots}
	ctx := WithPeerChainVerifier(WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{self}}), v)
	if _, err := VerifiedPeerCertificate(ctx); !errors.Is(err, ErrInvalidCertificate) {
		t.Fatalf("err = %v, want ErrInvalidCertificate", err)
	}
	if v.calls != 1 {
		t.Fatalf("verifier calls = %d, want 1", v.calls)
	}
}

func TestWithPeerChainVerifier_NilLeavesContext(t *testing.T) {
	ctx := context.Background()
	if WithPeerChainVerifier(ctx, nil) != ctx {
		t.Fatal("a nil verifier changed the context")
	}
}
