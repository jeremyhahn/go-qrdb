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

import type { Client } from "@qrdb/sdk";

/**
 * Reports whether err says the server has no verifiable-log service: a REST
 * 501 Not Implemented, the SDK's NotImplementedError, or the desktop
 * binding's embedded NilServiceError ("embedded: <service> must not be nil").
 * Any other failure (a refusal, a network error) leaves the service present,
 * so the view can show it.
 */
export function isLogServiceAbsent(err: unknown): boolean {
  if (typeof err === "string") return /must not be nil|\b501\b|not implemented/i.test(err);
  if (typeof err !== "object" || err === null) return false;
  const e = err as { name?: unknown; statusCode?: unknown; status?: unknown; message?: unknown };
  if (e.name === "NotImplementedError" || e.name === "NilServiceError") return true;
  if (e.statusCode === 501 || e.status === 501) return true;
  const msg = typeof e.message === "string" ? e.message : "";
  return /must not be nil|\b501\b|not implemented/i.test(msg);
}

/**
 * Asks the server for the database's logs and reports whether it has a
 * verifiable-log service. Only an answer that says the service is absent
 * (see {@link isLogServiceAbsent}) reports false.
 */
export async function probeLogService(client: Client): Promise<boolean> {
  try {
    await client.vlog.list();
    return true;
  } catch (err) {
    return !isLogServiceAbsent(err);
  }
}
