package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestGRPCWeb_OffByDefault(t *testing.T) {
	if got := DefaultConfig().Server.GRPCWeb; !reflect.DeepEqual(got, GRPCWebConfig{}) {
		t.Errorf("default server.grpc_web = %+v, want the zero (off) value", got)
	}
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.GRPCWeb.Enabled {
		t.Error("a file without server.grpc_web enabled gRPC-Web")
	}
}

func TestGRPCWeb_LoadsTheYAMLBlock(t *testing.T) {
	yaml := strings.Replace(validYAML, "server:\n", `server:
  grpc_web:
    enabled: true
    allowed_origins: ["https://app.example.com", "http://localhost:3000"]
    allow_credentials: true
`, 1)
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := GRPCWebConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"https://app.example.com", "http://localhost:3000"},
		AllowCredentials: true,
	}
	if !reflect.DeepEqual(cfg.Server.GRPCWeb, want) {
		t.Errorf("server.grpc_web = %+v, want %+v", cfg.Server.GRPCWeb, want)
	}
}

func TestGRPCWeb_UnknownKeyRefused(t *testing.T) {
	yaml := strings.Replace(validYAML, "server:\n", "server:\n  grpc_web:\n    enable: true\n", 1)
	_, err := Load(writeTemp(t, yaml))
	var unknown *UnknownFieldError
	if !errors.As(err, &unknown) || unknown.Path != "server.grpc_web.enable" {
		t.Fatalf("Load = %v, want an *UnknownFieldError for server.grpc_web.enable", err)
	}
}

// TestGRPCWeb_Validate pins what Validate judges of server.grpc_web: that an
// enabled block has the gRPC server it is served through. The CORS fields are
// the gRPC-Web handler's to judge, at Start (pkg/server).
func TestGRPCWeb_Validate(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*Config)
		field  string
	}{
		"enabled": {mutate: func(c *Config) { c.Server.GRPCWeb = GRPCWebConfig{Enabled: true, AllowedOrigins: []string{"*"}} }},
		"enabled without gRPC": {
			mutate: func(c *Config) {
				c.Server.GRPCWeb.Enabled = true
				c.Protocols.GRPC = false
			},
			field: "server.grpc_web.enabled",
		},
		"disabled without gRPC": {mutate: func(c *Config) { c.Protocols.GRPC = false }},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)
			err := cfg.Validate()
			if tc.field == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			var verr *ValidationError
			if !errors.As(err, &verr) || verr.Field != tc.field {
				t.Fatalf("Validate = %v, want a *ValidationError for %s", err, tc.field)
			}
		})
	}
}

func TestValidationError_Unwrap(t *testing.T) {
	cause := errors.New("cause")
	if got := (&ValidationError{Field: "f", Reason: "r", Err: cause}).Unwrap(); got != cause {
		t.Errorf("Unwrap = %v, want the cause", got)
	}
	if got := (&ValidationError{Field: "f", Reason: "r"}).Unwrap(); got != nil {
		t.Errorf("Unwrap = %v, want nil", got)
	}
}
