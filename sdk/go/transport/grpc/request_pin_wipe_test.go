package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRequestPINWipe: the gRPC generic Request consumes the request's PIN
// buffers, so a refusal before dispatch (closed transport) still wipes them.
func TestRequestPINWipe(t *testing.T) {
	tr := &Transport{}
	tr.closed.Store(true)
	r := &transport.CeremonyInitializeRequest{SOPin: transport.Secret("123456"), UserPin: transport.Secret("654321")}
	err := tr.Request(context.Background(), "/no.Such/Method", r, nil)
	var closed *transport.ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("Request error = %v, want ClosedError", err)
	}
	for i, p := range []transport.Secret{r.SOPin, r.UserPin} {
		for _, b := range p {
			if b != 0 {
				t.Fatalf("PIN %d not wiped: %v", i, []byte(p))
			}
		}
	}
}
