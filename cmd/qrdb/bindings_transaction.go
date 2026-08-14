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

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TransactionBinding exposes atomic compare-and-swap transaction
// operations to the Wails frontend.
type TransactionBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewTransactionBinding creates a new TransactionBinding backed by the
// given connection.
func NewTransactionBinding(log *logger.Logger, conn *ConnectionBinding) *TransactionBinding {
	return &TransactionBinding{logger: log, conn: conn}
}

// Execute atomically evaluates compares and executes the corresponding
// operation set. When all compares succeed, success ops are applied;
// otherwise failure ops are applied.
func (b *TransactionBinding) Execute(req *transport.TransactionRequest) (*transport.TransactionResponse, error) {
	b.logger.Debug("Execute called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Execute failed", "error", err)
		return nil, err
	}
	resp, err := cl.Transaction().ExecuteTransaction(context.Background(), req)
	if err != nil {
		b.logger.Error("Execute failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Execute completed")
	return resp, nil
}
