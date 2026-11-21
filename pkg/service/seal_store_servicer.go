// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// SealStoreServicer is the node's seal store as a network listener serves
// it: the named-secret store beneath the node barrier. Every route over it
// is gated by permission.SealStoreManage (seal:store_manage, ScopeCross),
// the permission the embedded SDK transport checks. *seal.SealStore
// satisfies it.
type SealStoreServicer interface {
	Put(ctx context.Context, name string, secret []byte) error
	Get(ctx context.Context, name string) ([]byte, error)
	Delete(ctx context.Context, name string) error
	List(ctx context.Context) ([]string, error)
	Reseal(ctx context.Context, name string) error
	Status(ctx context.Context) (*seal.SealStoreStatus, error)
}

var _ SealStoreServicer = (*seal.SealStore)(nil)
