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

// Package vanguardprobe reproduces the blocker that keeps pkg/api/grpcweb
// hand-rolled (#1151, docs/api-server/architecture.md): a gRPC service
// served through connectrpc.com/vanguard/vanguardgrpc runs outside the
// grpc.Server's interceptor chain, and its handler's peer carries no TLS
// state, so the client certificate qrdb authenticates is lost.
//
// Each test asserts the behaviour of the pinned vanguard release and fails,
// naming #1151 step 3, the day a release no longer has it. `make
// check-vanguard-probe` runs it; it is the check to run at every vanguard
// release. The probe is a module of its own so that vanguard never enters
// the database's module graph, and no root `./...` pattern or `make test`
// reaches it.
package vanguardprobe
