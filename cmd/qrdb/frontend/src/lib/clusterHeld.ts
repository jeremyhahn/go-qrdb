// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { TransportError } from "@qrdb/sdk";

/**
 * Whether a seal status call was refused with 503 SEALED: the node barrier
 * is unsealed, but a cluster seal holds the node (or it cannot confirm the
 * seal records), so it answers no status question and admits only the
 * authenticated cluster unseal.
 *
 * The SDK reduces the server's {"error":"barrier sealed","code":"SEALED"}
 * body to its error text, so the refusal reads "...: barrier sealed".
 */
export function isClusterHeldError(err: unknown): boolean {
  return (
    err instanceof TransportError &&
    err.statusCode === 503 &&
    (err.message.endsWith(": barrier sealed") || err.message.includes('"code":"SEALED"'))
  );
}
