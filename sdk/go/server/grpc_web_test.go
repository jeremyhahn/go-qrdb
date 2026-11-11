package server_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	sdkserver "github.com/jeremyhahn/go-qrdb/sdk/go/server"
)

// Compile-time: the SDK names are the server's declarations, not copies, so
// an embedder configures and recognizes gRPC-Web without importing pkg/*.
var (
	_ config.GRPCWebConfig                                 = sdkserver.GRPCWebConfig{}
	_ *config.ValidationError                              = (*sdk.DaemonConfigValidationError)(nil)
	_ error                                                = (*sdkserver.GRPCWebUnavailableError)(nil)
	_ error                                                = (*sdkserver.RESTGRPCWebRouteCollisionError)(nil)
	_ func(sdkserver.GRPCWebConfig) sdkserver.ServerOption = sdkserver.WithGRPCWeb
)

func TestSDK_GRPCWeb_ZeroConfigIsOff(t *testing.T) {
	if (sdkserver.GRPCWebConfig{}).Enabled {
		t.Error("the zero GRPCWebConfig is enabled")
	}
	if sdkserver.WithGRPCWeb(sdkserver.GRPCWebConfig{Enabled: true}) == nil {
		t.Error("WithGRPCWeb returned no option")
	}
}
