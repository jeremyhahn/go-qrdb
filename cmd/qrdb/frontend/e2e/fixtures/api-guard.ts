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

// Shared base fixture for the mocked Playwright tier.
//
// The mocked tier runs the UI against the Vite dev server with no qrdb server
// behind its proxy, so a request no mock answers is refused by the proxy. The
// UI tolerates many such failures (a fallback form, a retry with backoff), so
// an unmocked request used to surface only as a timeout far from its cause,
// and only under load. This fixture makes it a named failure instead.
//
// It registers a catch-all route for every API path (api-routes.ts) BEFORE any
// spec or mock fixture installs its own routes. Playwright runs the most
// recently registered matching handler first, so every specific mock still
// answers its own requests, and a handler that declines one with
// route.fallback() hands it here. This handler records the request and aborts
// it as the dead proxy would; the fixture's teardown then fails the test,
// naming each unmocked request.
//
// Every mocked-tier fixture and spec extends `test` from this file, never
// @playwright/test's directly.

import { test as base } from "@playwright/test";
import { isApiPath } from "../../api-routes.ts";

export const test = base.extend({
  page: async ({ page }, use) => {
    const unmocked: string[] = [];
    await page.route(
      (url) => isApiPath(url.pathname),
      async (route) => {
        const request = route.request();
        unmocked.push(`${request.method()} ${new URL(request.url()).pathname}`);
        await route.abort("connectionrefused");
      },
    );
    await use(page);
    if (unmocked.length > 0) {
      throw new Error(
        `unmocked API request(s) reached the network; mock each one in the spec or its fixture:\n  ${[
          ...new Set(unmocked),
        ].join("\n  ")}`,
      );
    }
  },
});
