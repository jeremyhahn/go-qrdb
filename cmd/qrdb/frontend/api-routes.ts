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

// API_PREFIXES lists the request paths the UI sends to the qrdb server.
//
// It is the one list two consumers share: vite.config.ts proxies each prefix
// to the server during development, and the mocked Playwright tier's API guard
// (e2e/fixtures/api-guard.ts) fails a test that sends a request under one of
// them without a mock to answer it. Keeping one list is what makes the guard
// complete: a prefix added for the proxy is guarded the moment it is added.
export const API_PREFIXES: readonly string[] = [
  "/health",
  "/status",
  "/peers",
  "/capabilities",
  "/kv",
  "/ts",
  "/tx",
  "/batch",
  "/watch",
  "/lease",
  "/lock",
  "/index",
  "/vector",
  "/certs",
  "/sql",
  "/snapshot",
  "/drain",
  "/membership",
  "/v1",
  "/users",
  "/invitations",
  "/roles",
  "/permissions",
  "/tenants",
  "/audit",
  "/barrier",
  "/ceremony",
  "/authfactor",
  "/databases",
  "/seal",
  "/collection",
  "/cluster",
  "/rebalance",
  "/backup",
  "/metrics",
  "/admin",
];

// isApiPath reports whether pathname is an API path: it equals a prefix or
// continues it with a path segment. Matching whole segments keeps app assets
// such as /index.html out of the API space.
export function isApiPath(pathname: string): boolean {
  return API_PREFIXES.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}
