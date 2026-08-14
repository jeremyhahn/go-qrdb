<!--
  Copyright 2026 Jeremy Hahn
  SPDX-License-Identifier: Apache-2.0

  Licensed under the Apache License, Version 2.0 (the "License");
  you may not use this file except in compliance with the License.
  You may obtain a copy of the License at

      http://www.apache.org/licenses/LICENSE-2.0

  Unless required by applicable law or agreed to in writing, software
  distributed under the License is distributed on an "AS IS" BASIS,
  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
  See the License for the specific language governing permissions and
  limitations under the License.
-->
<script lang="ts">
  type StatusVariant = "success" | "warning" | "error" | "info";

  let {
    label,
    variant = "info",
    pulse = false,
  }: {
    label: string;
    variant?: StatusVariant;
    pulse?: boolean;
  } = $props();

  const variantClasses: Readonly<Record<StatusVariant, string>> = {
    success: "badge-success",
    warning: "badge-warning",
    error: "badge-error",
    info: "badge-info",
  } as const;

  const dotColors: Readonly<Record<StatusVariant, string>> = {
    success: "bg-success",
    warning: "bg-warning",
    error: "bg-error",
    info: "bg-info",
  } as const;
</script>

<span class="badge badge-sm gap-1.5 {variantClasses[variant]}">
  <span
    class="inline-block h-1.5 w-1.5 rounded-full {dotColors[variant]}"
    class:animate-pulse={pulse}
  ></span>
  {label}
</span>
