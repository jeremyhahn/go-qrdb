// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
)

// errorBackend is a StorageBackend that returns configurable errors.
type errorBackend struct {
	getErr    error
	putErr    error
	deleteErr error
	listErr   error
	scanErr   error
	existsErr error
}

func (e *errorBackend) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, e.getErr
}
func (e *errorBackend) Put(_ context.Context, _ string, _ []byte) error {
	return e.putErr
}
func (e *errorBackend) PutIfAbsent(_ context.Context, _ string, _ []byte) (bool, error) {
	if e.putErr != nil {
		return false, e.putErr
	}
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (e *errorBackend) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}
func (e *errorBackend) Delete(_ context.Context, _ string) error {
	return e.deleteErr
}
func (e *errorBackend) List(_ context.Context, _ string) ([]string, error) {
	return nil, e.listErr
}
func (e *errorBackend) Scan(_ context.Context, _ string, _ func(string, []byte) error) error {
	return e.scanErr
}
func (e *errorBackend) Exists(_ context.Context, _ string) (bool, error) {
	return false, e.existsErr
}
func (e *errorBackend) Close() error { return nil }
