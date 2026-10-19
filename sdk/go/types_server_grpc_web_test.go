package sdk_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// Compile-time: the SDK names are the server's declarations, not copies, so
// an embedder configures and recognizes gRPC-Web without importing pkg/*.
var (
	_ config.GRPCWebConfig                     = sdk.GRPCWebConfig{}
	_ *config.ValidationError                  = (*sdk.DaemonConfigValidationError)(nil)
	_ error                                    = (*sdk.GRPCWebUnavailableError)(nil)
	_ error                                    = (*sdk.RESTGRPCWebRouteCollisionError)(nil)
	_ func(sdk.GRPCWebConfig) sdk.ServerOption = sdk.WithGRPCWeb
)

func TestSDK_GRPCWeb_ZeroConfigIsOff(t *testing.T) {
	if (sdk.GRPCWebConfig{}).Enabled {
		t.Error("the zero GRPCWebConfig is enabled")
	}
	if sdk.WithGRPCWeb(sdk.GRPCWebConfig{Enabled: true}) == nil {
		t.Error("WithGRPCWeb returned no option")
	}
}
