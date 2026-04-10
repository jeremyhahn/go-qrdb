//go:build qrdb_vector

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

package service

import (
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
)

// mockLogger implements interfaces.Logger for testing.
type mockLogger struct {
	fields map[string]interface{}
}

func newMockLogger() *mockLogger {
	return &mockLogger{
		fields: make(map[string]interface{}),
	}
}

func (l *mockLogger) Debug(msg string, args ...any)                              {}
func (l *mockLogger) Debugf(format string, args ...interface{})                  {}
func (l *mockLogger) Info(msg string, args ...any)                               {}
func (l *mockLogger) Infof(format string, args ...interface{})                   {}
func (l *mockLogger) Warn(msg string, args ...any)                               {}
func (l *mockLogger) Warnf(format string, args ...interface{})                   {}
func (l *mockLogger) Error(msg string, args ...any)                              {}
func (l *mockLogger) Errorf(format string, args ...interface{})                  {}
func (l *mockLogger) Fatal(msg string)                                           {}
func (l *mockLogger) Fatalf(format string, args ...interface{})                  {}
func (l *mockLogger) WithField(key string, value interface{}) interfaces.Logger  { return l }
func (l *mockLogger) WithFields(fields map[string]interface{}) interfaces.Logger { return l }
func (l *mockLogger) WithError(err error) interfaces.Logger                      { return l }
