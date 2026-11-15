// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { nextTick } from "vue";
import { StatCard } from "nanocat-ui";
import Dashboard from "../Dashboard.vue";
import { statsApi } from "@/api/stats";

vi.hoisted(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response("{}", { status: 404 })),
  );
});
vi.mock("@/api/stats", () => ({ statsApi: { overview: vi.fn() } }));

const overview: Awaited<ReturnType<typeof statsApi.overview>> = {
  total_accounts: 8,
  active_accounts: 8,
  total_quota: 2025,
  abnormal_accounts: 0,
  disabled_accounts: 0,
  failed_accounts: 0,
  rate_limited_accounts: 0,
  idle_accounts: 0,
  trend: {
    labels: ["21:00", "22:00"],
    total_requests: [3, 7],
    success_requests: [3, 6],
    failed_requests: [0, 1],
    rate_limited_requests: [0, 0],
    model_requests: { "gpt-image-2.5": [3, 7] },
    model_ttfb_times: { "gpt-image-2.5": [100, 200] },
    model_total_times: { "gpt-image-2.5": [1000, 2000] },
  },
};
const instances: Array<{
  element: HTMLElement;
  setOption: ReturnType<typeof vi.fn>;
  resize: ReturnType<typeof vi.fn>;
  dispose: ReturnType<typeof vi.fn>;
}> = [];
let wrapper: VueWrapper | undefined;

beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  vi.mocked(statsApi.overview).mockResolvedValue(overview);
  vi.stubGlobal("echarts", {
    init: vi.fn((element: HTMLElement) => {
      const chart = {
        element,
        setOption: vi.fn(),
        resize: vi.fn(),
        dispose: vi.fn(),
        on: vi.fn(),
        off: vi.fn(),
      };
      instances.push(chart);
      return chart;
    }),
  });
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  instances.length = 0;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  vi.useRealTimers();
});

async function settleCharts() {
  await flushPromises();
  await nextTick();
  await vi.advanceTimersByTimeAsync(100);
}

describe("dashboard chart rendering", () => {
  it("renders all six charts into mounted DOM elements after loading real page data", async () => {
    wrapper = mount(Dashboard, { global: { stubs: { Icon: true } } });
    await settleCharts();
    expect(wrapper.findAllComponents(StatCard)[0].props("value")).toBe("8");
    expect(instances).toHaveLength(6);
    expect(new Set(instances.map((chart) => chart.element)).size).toBe(6);
    for (const chart of instances) {
      expect(wrapper.element.contains(chart.element)).toBe(true);
      expect(chart.setOption).toHaveBeenCalled();
      const option = chart.setOption.mock.calls[0][0];
      expect(option.series.length).toBeGreaterThan(0);
    }
    const renderCounts = instances.map((chart) => chart.setOption.mock.calls.length);
    await wrapper.get("button").trigger("click");
    await flushPromises();
    expect(instances).toHaveLength(6);
    instances.forEach((chart, index) =>
      expect(chart.setOption.mock.calls.length).toBeGreaterThan(renderCounts[index]),
    );
    window.dispatchEvent(new Event("resize"));
    for (const chart of instances) expect(chart.resize).toHaveBeenCalled();
    wrapper.unmount();
    wrapper = undefined;
    for (const chart of instances) expect(chart.dispose).toHaveBeenCalledTimes(1);
  });

  it("binds new chart elements again when returning to a visible dashboard", async () => {
    wrapper = mount(Dashboard, { global: { stubs: { Icon: true } } });
    await settleCharts();
    expect(instances).toHaveLength(6);
    const previous = [...instances];
    vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    document.dispatchEvent(new Event("visibilitychange"));
    await nextTick();
    for (const chart of previous) expect(chart.dispose).toHaveBeenCalledTimes(1);
    vi.spyOn(document, "hidden", "get").mockReturnValue(false);
    document.dispatchEvent(new Event("visibilitychange"));
    await settleCharts();
    expect(instances).toHaveLength(12);
    for (const chart of instances.slice(6)) {
      expect(wrapper.element.contains(chart.element)).toBe(true);
      expect(previous.some((old) => old.element === chart.element)).toBe(false);
      expect(chart.setOption).toHaveBeenCalled();
    }
  });
});
