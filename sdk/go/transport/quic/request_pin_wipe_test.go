package quic

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRequestPINWipe: the QUIC generic Request is not implemented, but it
// still wipes the PIN buffers the caller handed it.
func TestRequestPINWipe(t *testing.T) {
	tr := newClosedTransport()
	r := &transport.BarrierInitializeRequest{SOPin: transport.Secret("123456"), UserPin: transport.Secret("654321")}
	if err := tr.Request(context.Background(), "BarrierInitialize", r, nil); err == nil {
		t.Fatal("Request succeeded, want NotImplemented")
	}
	for i, p := range []transport.Secret{r.SOPin, r.UserPin} {
		for _, b := range p {
			if b != 0 {
				t.Fatalf("PIN %d not wiped: %v", i, []byte(p))
			}
		}
	}
}
