package quic

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestBearer_QUICNewRefusesTokenSource(t *testing.T) {
	for name, opt := range map[string]transport.Option{
		"WithBearerToken": transport.WithBearerToken("host-issued-token"),
		"WithTokenSource": transport.WithTokenSource(transport.StaticTokenSource("host-issued-token")),
	} {
		_, err := New(transport.WithAddress("127.0.0.1:1"), opt)
		var cfgErr *transport.ConfigError
		if !errors.As(err, &cfgErr) || cfgErr.Field != "TokenSource" {
			t.Fatalf("%s: err = %v, want *transport.ConfigError{Field: TokenSource}", name, err)
		}
	}
	if _, err := New(transport.WithAddress("127.0.0.1:1")); err != nil {
		t.Fatalf("New without a bearer: %v", err)
	}
}
