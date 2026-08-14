<!-- Copyright 2026 Jeremy Hahn -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
<!--
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
  /** Step labels to display. */
  interface Props {
    steps: readonly string[];
    currentIndex: number;
  }

  let { steps, currentIndex }: Props = $props();
</script>

<ul class="steps w-full mb-6">
  {#each steps as label, i}
    <li class="step" class:step-primary={i <= currentIndex}>
      {label}
    </li>
  {/each}
</ul>
