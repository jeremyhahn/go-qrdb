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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/service"

// NotLeaderError is the service layer's not-leader condition, re-exported
// so embedding hosts can classify it with errors.As (e.g. to retry
// bootstrap work while a cluster's initial election settles) without
// importing pkg/service.
type NotLeaderError = service.NotLeaderError
