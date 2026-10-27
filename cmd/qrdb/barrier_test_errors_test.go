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

// barrier_test_errors_test.go holds the barrier test sentinel shared by
// barrier_cmd_test.go (!integration) and bindings_seal_test.go
// (qrdb_embedded && !integration). Its constraint is the union of both
// callers', so no tag set deletes it while keeping a caller.
package main

import "errors"

// errBarrierTest is a sentinel error returned by mock barrier functions.
var errBarrierTest = errors.New("barrier test failure")
