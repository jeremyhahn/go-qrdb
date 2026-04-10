// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !integration

package etcd

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *database.EtcdConfig
		wantErr   bool
		errTarget any
	}{
		{
			name:      "NilConfig",
			cfg:       nil,
			wantErr:   true,
			errTarget: &InvalidRequestError{},
		},
		{
			name: "EnabledWithListenAddr",
			cfg: &database.EtcdConfig{
				Enabled:    true,
				ListenAddr: "127.0.0.1:2379",
			},
			wantErr: false,
		},
		{
			name: "DisabledNoAddr",
			cfg: &database.EtcdConfig{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name: "EnabledMissingListenAddr",
			cfg: &database.EtcdConfig{
				Enabled:    true,
				ListenAddr: "",
			},
			wantErr:   true,
			errTarget: &database.EtcdListenAddrRequiredError{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.errTarget != nil {
					switch target := tc.errTarget.(type) {
					case *InvalidRequestError:
						var got *InvalidRequestError
						if !errors.As(err, &got) {
							t.Errorf("expected *InvalidRequestError, got %T: %v", err, err)
						}
					case *database.EtcdListenAddrRequiredError:
						_ = target
						var got *database.EtcdListenAddrRequiredError
						if !errors.As(err, &got) {
							t.Errorf("expected *EtcdListenAddrRequiredError, got %T: %v", err, err)
						}
					}
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidateConfig_NilReturnsInvalidRequestDetail(t *testing.T) {
	err := ValidateConfig(nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
	var target *InvalidRequestError
	if !errors.As(err, &target) {
		t.Fatalf("expected *InvalidRequestError, got %T", err)
	}
	if target.Detail != "etcd config is nil" {
		t.Errorf("Detail = %q, want %q", target.Detail, "etcd config is nil")
	}
}

func TestValidateConfig_EnabledWithWhitespaceAddr(t *testing.T) {
	cfg := &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: "   ",
	}
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for whitespace-only listen addr")
	}
	var target *database.EtcdListenAddrRequiredError
	if !errors.As(err, &target) {
		t.Errorf("expected *EtcdListenAddrRequiredError, got %T: %v", err, err)
	}
}
