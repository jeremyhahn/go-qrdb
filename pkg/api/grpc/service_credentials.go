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

package grpc

// Credential gRPC handlers will be implemented here once the protobuf
// service definition includes credential management RPCs. The methods below
// will map 1:1 to the CredentialServicer interface:
//
//   - CredentialSubmit   -> CredentialServicer.CredentialSubmit
//   - CredentialStrategy -> CredentialServicer.CredentialStrategy
//   - CredentialDelete   -> CredentialServicer.CredentialDelete
//
// The CredentialServicer is already wired into the qrdbServiceServer struct
// via the WithCredentialService server option and WithRegisterCredentialService
// register option.
