package server_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	restapi "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbserver "github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/sdk/go/server"
)

// The re-exports are aliases of the REST types, not copies.
var (
	_ *restapi.WebTransportSession             = (*server.WebTransportSession)(nil)
	_ restapi.WebTransportHandler              = server.WebTransportHandler(nil)
	_ *restapi.InvalidWebTransportHandlerError = (*server.InvalidWebTransportHandlerError)(nil)
	_ error                                    = (*server.InvalidWebTransportHandlerError)(nil)
	_ restapi.WebTransportStreamErrorCode      = server.WebTransportStreamErrorCode(0)
	_ restapi.WebTransportSessionErrorCode     = server.WebTransportSessionErrorCode(0)
	_ *restapi.WebTransportStreamError         = (*server.WebTransportStreamError)(nil)
	_ *restapi.WebTransportSessionError        = (*server.WebTransportSessionError)(nil)
	_ error                                    = (*server.WebTransportStreamError)(nil)
	_ error                                    = (*server.WebTransportSessionError)(nil)
)

func TestWithWebTransportHandler_ReturnsAServerOption(t *testing.T) {
	h := func(*server.WebTransportSession, *auth.Identity, string) {}
	var opt qrdbserver.ServerOption = server.WithWebTransportHandler("/wt", h)
	if opt == nil {
		t.Fatal("WithWebTransportHandler returned a nil option")
	}
	if server.WithWebTransportHandler("", nil) == nil {
		t.Fatal("WithWebTransportHandler returned a nil option for an invalid handler")
	}
}

func TestInvalidWebTransportHandlerError_NamesThePath(t *testing.T) {
	var err error = &server.InvalidWebTransportHandlerError{Path: "wt", NilHandler: true}
	if !strings.Contains(err.Error(), strconv.Quote("wt")) {
		t.Fatalf("error %q does not name the path", err)
	}
	var target *restapi.InvalidWebTransportHandlerError
	if !errors.As(err, &target) || !target.NilHandler {
		t.Fatalf("errors.As did not recover the REST error from %v", err)
	}
}
