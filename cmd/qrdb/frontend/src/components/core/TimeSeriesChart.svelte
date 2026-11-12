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
  import { init, use } from "echarts/core";
  import { LineChart, ScatterChart } from "echarts/charts";
  import {
    GridComponent,
    TooltipComponent,
    DataZoomComponent,
    ToolboxComponent,
  } from "echarts/components";
  import { CanvasRenderer } from "echarts/renderers";
  import type { ECharts, EChartsCoreOption } from "echarts/core";

  use([
    LineChart,
    ScatterChart,
    GridComponent,
    TooltipComponent,
    DataZoomComponent,
    ToolboxComponent,
    CanvasRenderer,
  ]);

  type ChartType = "line" | "area" | "scatter";

  interface DataPoint {
    timestamp: number;
    value: number;
    metric?: string;
  }

  let {
    data,
    chartType = "line",
    height = 320,
    loading = false,
    accentColor = "#22d3ee",
  }: {
    data: DataPoint[];
    chartType?: ChartType;
    height?: number;
    loading?: boolean;
    accentColor?: string;
  } = $props();

  let chartContainer: HTMLDivElement = $state(null!);

  // Non-reactive chart reference — using $state here causes an infinite
  // $effect loop because the init effect both reads and writes the value.
  let chartRef: ECharts | null = null;

  /** Builds the ECharts option from current props. */
  function buildOption(): EChartsCoreOption {
    const seriesData = data.map((point) => [point.timestamp, point.value]);

    const isScatter = chartType === "scatter";
    const isArea = chartType === "area";

    return {
      backgroundColor: "transparent",
      textStyle: {
        color: "rgba(255, 255, 255, 0.6)",
      },
      grid: {
        left: 48,
        right: 24,
        top: 24,
        bottom: 64,
        containLabel: false,
      },
      xAxis: {
        type: "time",
        axisLine: {
          lineStyle: { color: "rgba(255, 255, 255, 0.2)" },
        },
        splitLine: {
          show: false,
        },
        axisLabel: {
          color: "rgba(255, 255, 255, 0.6)",
          fontSize: 11,
        },
      },
      yAxis: {
        type: "value",
        axisLine: {
          show: false,
        },
        splitLine: {
          lineStyle: { color: "rgba(255, 255, 255, 0.1)" },
        },
        axisLabel: {
          color: "rgba(255, 255, 255, 0.6)",
          fontSize: 11,
        },
      },
      tooltip: {
        trigger: isScatter ? "item" : "axis",
        backgroundColor: "rgba(0, 0, 0, 0.8)",
        borderColor: "rgba(255, 255, 255, 0.1)",
        textStyle: {
          color: "rgba(255, 255, 255, 0.85)",
          fontSize: 12,
        },
      },
      dataZoom: [
        {
          type: "slider",
          bottom: 8,
          height: 20,
          borderColor: "rgba(255, 255, 255, 0.1)",
          backgroundColor: "rgba(255, 255, 255, 0.04)",
          fillerColor: "rgba(99, 102, 241, 0.15)",
          handleStyle: {
            color: accentColor,
            borderColor: accentColor,
          },
          textStyle: {
            color: "rgba(255, 255, 255, 0.4)",
            fontSize: 10,
          },
          dataBackground: {
            lineStyle: { color: "rgba(255, 255, 255, 0.1)" },
            areaStyle: { color: "rgba(255, 255, 255, 0.04)" },
          },
        },
      ],
      toolbox: {
        feature: {
          saveAsImage: {
            title: "Save",
            iconStyle: {
              borderColor: "rgba(255, 255, 255, 0.4)",
            },
          },
          dataZoom: {
            title: {
              zoom: "Zoom",
              back: "Reset",
            },
            iconStyle: {
              borderColor: "rgba(255, 255, 255, 0.4)",
            },
          },
        },
        right: 12,
        top: 0,
      },
      series: [
        {
          type: isScatter ? "scatter" : "line",
          data: seriesData,
          smooth: !isScatter,
          showSymbol: isScatter,
          symbolSize: isScatter ? 6 : 4,
          itemStyle: {
            color: accentColor,
          },
          lineStyle: isScatter
            ? undefined
            : {
                color: accentColor,
                width: 3,
              },
          areaStyle: isArea ? { color: accentColor, opacity: 0.2 } : undefined,
        },
      ],
    };
  }

  // Initialize/dispose chart when container mounts/unmounts.
  // Only reads chartContainer (reactive via bind:this), not chartRef.
  $effect(() => {
    const container = chartContainer;
    if (!container) return;

    chartRef = init(container, undefined, { renderer: "canvas" });
    chartRef.setOption(buildOption());

    const handleResize = (): void => chartRef?.resize();
    window.addEventListener("resize", handleResize);

    return () => {
      window.removeEventListener("resize", handleResize);
      chartRef?.dispose();
      chartRef = null;
    };
  });

  // Update chart options when data or configuration changes
  $effect(() => {
    if (!chartRef || loading) return;
    // Read reactive props to track them as dependencies
    const _d = data;
    const _t = chartType;
    const _c = accentColor;
    void _d;
    void _t;
    void _c;
    chartRef.setOption(buildOption(), true);
  });
</script>

<div class="relative" style="height: {height}px;">
  {#if loading}
    <div class="skeleton h-full w-full rounded-lg"></div>
  {:else}
    <div
      bind:this={chartContainer}
      style="width: 100%; height: {height}px;"
    ></div>
  {/if}
</div>
