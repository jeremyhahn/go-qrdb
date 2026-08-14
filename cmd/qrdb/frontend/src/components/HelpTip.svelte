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
  let { text, position = "top" }: { text: string; position?: "top" | "bottom" | "left" | "right" } = $props();
</script>

<span
  class="tooltip tooltip-{position} inline-flex cursor-help"
  data-tip={text}
>
  <span class="inline-flex h-4 w-4 items-center justify-center rounded-full border border-base-content/20 text-[10px] font-medium text-base-content/40 hover:border-base-content/40 hover:text-base-content/60">
    ?
  </span>
</span>
