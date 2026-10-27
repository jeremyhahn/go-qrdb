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

//go:build !qrdb_vlog

package service

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

func TestNewLogService_NotCompiled(t *testing.T) {
	svc, err := NewLogService(LogServiceConfig{Resolver: &fixedShardResolver{}})
	var notCompiled *vlog.NotCompiledError
	if svc != nil || !errors.As(err, &notCompiled) {
		t.Fatalf("NewLogService = %v, %v; want NotCompiledError", svc, err)
	}
}

func TestNewLogService_NotCompiledIsACapabilityError(t *testing.T) {
	_, err := NewLogService(LogServiceConfig{})
	var capability protocol.CapabilityError
	if !errors.As(err, &capability) {
		t.Fatalf("err = %v, want a protocol.CapabilityError", err)
	}
}
