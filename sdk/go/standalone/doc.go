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

// Package standalone composes a flat single-node QRDB server in process:
// NewServer opens the configured storage backend and engine, the optional
// seal barrier and audit trail, and the lease-expiry sweeper, and returns a
// Server whose Client talks to them through the embedded transport.
//
// It is a package of its own, rather than part of sdk/go/transport/embedded,
// so that an application which composes its node some other way (sdk/go/node)
// and only needs the transport does not compile this composer's storage
// engines and node lifecycle.
package standalone
