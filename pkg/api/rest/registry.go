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

package rest

import "github.com/jeremyhahn/go-qrdb/pkg/service"

// ServiceRegistry resolves per-(tenant, database) service instances for
// data-plane request routing. It is the REST-package alias of the
// transport-neutral service.Registry so that the same registry implementation
// is shared by every API protocol (REST, gRPC, Unix, QUIC, MCP) without
// duplicating the namespace/resolve logic.
//
// Implementations must be safe for concurrent use.
type ServiceRegistry = service.Registry
