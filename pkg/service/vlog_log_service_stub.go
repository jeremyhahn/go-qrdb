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

import "github.com/jeremyhahn/go-qrdb/pkg/vlog"

// NewLogService refuses in a build without qrdb_vlog: the named-log service
// is not compiled in, and the composer leaves it out.
func NewLogService(cfg LogServiceConfig) (LogService, error) {
	return nil, &vlog.NotCompiledError{}
}
