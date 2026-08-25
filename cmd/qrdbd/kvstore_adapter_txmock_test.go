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

package main

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// mockTxServicer is the transaction plane these bootstrap tests need only to
// EXIST. The custodian service refuses to construct without a store that can
// apply a guarded multi-key write (go-qrdb#21), so bootstrapAuthEnforcement
// cannot complete without one — and passing nil is not a shortcut, because the
// guarded adapter deliberately degrades to the plain one on a nil servicer so
// the capability assertion stays honest.
//
// It reports every transaction as succeeded. No test here registers a
// custodian, so no test depends on that answer; a test that did would need a
// fixture that actually evaluates the compare, since "always succeeded" is
// precisely what a broken fence looks like.
type mockTxServicer struct{}

var _ service.TransactionServicer = (*mockTxServicer)(nil)

func (*mockTxServicer) Execute(context.Context, []service.Compare, []service.Op, []service.Op) (*service.TransactionResponse, error) {
	return &service.TransactionResponse{Succeeded: true}, nil
}

func (*mockTxServicer) ExecuteWithMode(context.Context, []service.Compare, []service.Op, []service.Op, writemode.Mode) (*service.TransactionResponse, error) {
	return &service.TransactionResponse{Succeeded: true}, nil
}

func (*mockTxServicer) ExecuteEntity(context.Context, *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return &service.TransactionEntityResponse{}, nil
}
