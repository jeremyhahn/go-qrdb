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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package grpc

import "google.golang.org/grpc"

// testhelpers_test.go holds test helpers shared across build-tag boundaries
// inside this package.
//
// newGRPCServer used to live in grpc_new_coverage_test.go, whose constraint is
// this same tag set plus `!integration`, while its caller grpc_coverage6_test.go
// carries the tag set WITHOUT that clause — so under BUILD_TAGS+integration the
// helper vanished and the package stopped typechecking (#203). This file
// carries the union of the callers' constraints, which is the tag set alone: a
// helper's constraint must be a SUPERSET of every caller's.
func newGRPCServer() *grpc.Server {
	return grpc.NewServer()
}
