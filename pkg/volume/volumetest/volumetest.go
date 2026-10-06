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

// Package volumetest is the volume.Service double every transport's
// volume-pool tests share. It is for tests only.
package volumetest

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// UnexpectedCallError is returned by a Service method whose func field is
// unset: the test reached a method it did not script.
type UnexpectedCallError struct {
	Method string
}

func (e *UnexpectedCallError) Error() string {
	return "volumetest: unexpected call to " + e.Method
}

// Service is a volume.Service whose pool methods delegate to optional func
// fields. An unset field fails T and returns *UnexpectedCallError; it reports
// with Errorf rather than Fatal because transports call the service from
// server goroutines, where Fatal cannot stop the test.
//
// MoveTenant and GetMove return *volume.MoveNotImplementedError, as the real
// catalog does until tenant moves land.
type Service struct {
	T                 testing.TB
	ListPoolsFn       func(context.Context) ([]volume.PoolView, error)
	GetPoolFn         func(context.Context, string) (*volume.PoolDetail, error)
	CreatePoolFn      func(context.Context, volume.CreatePoolRequest) (*volume.Pool, error)
	UpdatePoolFn      func(context.Context, string, volume.UpdatePoolRequest) (*volume.Pool, error)
	DeletePoolFn      func(context.Context, string) error
	ListAssignmentsFn func(context.Context, string, string, int) (*volume.AssignmentPage, error)
}

var _ volume.Service = (*Service)(nil)

// unexpected reports a call to an unscripted method and returns the error the
// method answers with.
func (s *Service) unexpected(method string) error {
	if s.T != nil {
		s.T.Helper()
		s.T.Errorf("volumetest: unexpected %s", method)
	}
	return &UnexpectedCallError{Method: method}
}

// ListPools implements volume.Service.
func (s *Service) ListPools(ctx context.Context) ([]volume.PoolView, error) {
	if s.ListPoolsFn == nil {
		return nil, s.unexpected("ListPools")
	}
	return s.ListPoolsFn(ctx)
}

// GetPool implements volume.Service.
func (s *Service) GetPool(ctx context.Context, name string) (*volume.PoolDetail, error) {
	if s.GetPoolFn == nil {
		return nil, s.unexpected("GetPool")
	}
	return s.GetPoolFn(ctx, name)
}

// CreatePool implements volume.Service.
func (s *Service) CreatePool(ctx context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
	if s.CreatePoolFn == nil {
		return nil, s.unexpected("CreatePool")
	}
	return s.CreatePoolFn(ctx, req)
}

// UpdatePool implements volume.Service.
func (s *Service) UpdatePool(ctx context.Context, name string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
	if s.UpdatePoolFn == nil {
		return nil, s.unexpected("UpdatePool")
	}
	return s.UpdatePoolFn(ctx, name, req)
}

// DeletePool implements volume.Service.
func (s *Service) DeletePool(ctx context.Context, name string) error {
	if s.DeletePoolFn == nil {
		return s.unexpected("DeletePool")
	}
	return s.DeletePoolFn(ctx, name)
}

// ListAssignments implements volume.Service.
func (s *Service) ListAssignments(ctx context.Context, pool, pageToken string, pageSize int) (*volume.AssignmentPage, error) {
	if s.ListAssignmentsFn == nil {
		return nil, s.unexpected("ListAssignments")
	}
	return s.ListAssignmentsFn(ctx, pool, pageToken, pageSize)
}

// MoveTenant implements volume.Service.
func (s *Service) MoveTenant(context.Context, string, string) (*volume.Move, error) {
	return nil, &volume.MoveNotImplementedError{}
}

// GetMove implements volume.Service.
func (s *Service) GetMove(context.Context, string) (*volume.Move, error) {
	return nil, &volume.MoveNotImplementedError{}
}
