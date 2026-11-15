import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { effectScope, ref } from "vue";
import type { PageRuntime } from "@/composables/usePageRuntime";
import { useAccountPageLifecycleRuntime } from "./accountPageLifecycleRuntime";

function fixture() {
  const events: Record<string, (context: { initial: boolean }) => void> = {};
  const timers = new Map<string, ReturnType<typeof setTimeout>>();
  const canRun = ref(true);
  const clearTimer = (key: string) => {
    clearTimeout(timers.get(key));
    timers.delete(key);
  };
  const runtime = {
    canRun,
    clearTimer,
    setTimer: (key: string, delay: number, callback: () => void) => {
      clearTimer(key);
      if (canRun.value) timers.set(key, setTimeout(callback, delay));
    },
    onActivate: (fn: (typeof events)[string]) => {
      events.activate = fn;
    },
    onDeactivate: (fn: (typeof events)[string]) => {
      events.deactivate = fn;
    },
    onShow: (fn: (typeof events)[string]) => {
      events.show = fn;
    },
    onHide: (fn: (typeof events)[string]) => {
      events.hide = fn;
    },
  } as unknown as PageRuntime;
  const loadData = vi.fn(async () => {});
  const busy = ref(false);
  const scope = effectScope();
  scope.run(() =>
    useAccountPageLifecycleRuntime({
      runtime,
      viewMode: ref("list"),
      pageSize: ref(20),
      currentPage: ref(1),
      keyword: ref(""),
      statusFilter: ref("all"),
      groupFilter: ref("all"),
      pageSizeDefault: 20,
      pageSizeOptions: [20],
      reloadTimerKey: "test",
      loadData,
      loadGroups: vi.fn(),
      invalidateData: vi.fn(),
      invalidateGroups: vi.fn(),
      clearSelection: vi.fn(),
      shouldSkipRefresh: () => busy.value,
    }),
  );
  return { events, canRun, loadData, busy, scope };
}

afterEach(() => vi.useRealTimers());
describe("account list automatic refresh", () => {
  it("polls only while visible and resumes after a busy dialog", async () => {
    vi.useFakeTimers();
    const f = fixture();
    f.events.activate({ initial: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(f.loadData).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(f.loadData).toHaveBeenCalledTimes(2);
    f.busy.value = true;
    await vi.advanceTimersByTimeAsync(10_000);
    expect(f.loadData).toHaveBeenCalledTimes(2);
    f.busy.value = false;
    await vi.advanceTimersByTimeAsync(10_000);
    expect(f.loadData).toHaveBeenCalledTimes(3);
    f.canRun.value = false;
    f.events.hide({ initial: false });
    await vi.advanceTimersByTimeAsync(30_000);
    expect(f.loadData).toHaveBeenCalledTimes(3);
    f.canRun.value = true;
    f.events.show({ initial: false });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(f.loadData).toHaveBeenCalledTimes(5);
    f.events.deactivate({ initial: false });
    f.scope.stop();
  });
  it("does not overlap slow polling requests", async () => {
    vi.useFakeTimers();
    const f = fixture();
    f.events.activate({ initial: true });
    await vi.advanceTimersByTimeAsync(0);
    let finish!: () => void;
    f.loadData.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    await vi.advanceTimersByTimeAsync(30_000);
    expect(f.loadData).toHaveBeenCalledTimes(2);
    finish();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(f.loadData).toHaveBeenCalledTimes(3);
    f.events.deactivate({ initial: false });
    f.scope.stop();
  });
});
