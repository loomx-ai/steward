import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { getScanLogs, streamScanEvents } from "@/api/client";
import type { JobLog, ScanLogPage } from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { formatScanLogMessage, ScanTaskEvents } from "./ScanTaskEvents";

vi.mock("@/api/client", () => ({
  getScanLogs: vi.fn(),
  streamScanEvents: vi.fn(),
}));

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  vi.useFakeTimers();
  vi.mocked(getScanLogs).mockReset().mockResolvedValue({
    items: [],
    live_cursor: "history-live",
  });
  vi.mocked(streamScanEvents).mockReset();
});

afterEach(() => vi.useRealTimers());

it("formats historical Alibaba Cloud operations with product names", () => {
  const formatted = formatScanLogMessage(
    'AlibabaCloud.DescribeDisks detail response requires unsupported pagination token "token"',
  );
  expect(formatted).toBe(
    'ecs DescribeDisks detail response requires unsupported pagination token "token"',
  );
  expect(formatted).not.toContain("AlibabaCloud.");
  expect(formatScanLogMessage("call ecs AlibabaCloud.DescribeDisks")).toBe(
    "call ecs DescribeDisks",
  );
  expect(formatScanLogMessage("AlibabaCloud.UnknownOperation failed")).toBe(
    "UnknownOperation failed",
  );
});

it("reconnects after the last received event cursor", async () => {
  const log = {
    id: "log-23456789abcdefgh",
    job_id: "job-23456789abcdefgh",
    aggregate_type: "scan_task",
    aggregate_id: "scn-23456789abcdefgh",
    target_key: "global",
    sequence: 1,
    kind: "text",
    level: "info",
    message: "Global scan completed",
    created_at: "2026-07-21T10:00:00Z",
  } satisfies JobLog;
  vi.mocked(streamScanEvents)
    .mockImplementationOnce(
      async (_connection, _scan, _targetKey, _after, onEvent) => {
        onEvent({ type: "log", data: log, id: "cursor-1" });
        throw new Error("connection dropped");
      },
    )
    .mockImplementationOnce(
      async (_connection, _scan, _targetKey, after, _onEvent, signal) => {
        expect(after).toBe("cursor-1");
        return new Promise<string>((_resolve, reject) => {
          signal.addEventListener(
            "abort",
            () => reject(new DOMException("aborted", "AbortError")),
            { once: true },
          );
        });
      },
    );
  const view = render(
    <QueryClientProvider client={new QueryClient()}>
      <LocaleProvider>
        <ScanTaskEvents
          connectionID="con-23456789abcdefgh"
          scanID="scn-23456789abcdefgh"
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  await act(async () => Promise.resolve());
  expect(screen.getByText("Global scan completed")).toBeVisible();
  expect(streamScanEvents).toHaveBeenCalledTimes(1);
  await act(async () => vi.advanceTimersByTimeAsync(1000));
  expect(streamScanEvents).toHaveBeenCalledTimes(2);
  view.unmount();
});

it("renders JSON only for cloud API log kinds", async () => {
  const textLog = {
    id: "log-23456789abcdefga",
    job_id: "job-23456789abcdefgh",
    aggregate_type: "scan_task",
    aggregate_id: "scn-23456789abcdefgh",
    target_key: "region:ap-northeast-1",
    sequence: 1,
    kind: "text",
    level: "info",
    message: "Tokyo scan completed",
    payload: { Hidden: "must-not-render" },
    created_at: "2026-07-23T13:38:30.386",
  } satisfies JobLog;
  const cloudLog = {
    ...textLog,
    id: "log-23456789abcdefgb",
    sequence: 2,
    kind: "cloud_api_response",
    message: "resource-center SearchResources returned",
    payload: { RequestId: "req-1", Resources: [{ ResourceId: "i-1" }] },
    created_at: "2026-07-23T13:38:31.004",
  } satisfies JobLog;
  vi.mocked(streamScanEvents).mockImplementation(
    async (_connection, _scan, targetKey, _after, onEvent, signal) => {
      expect(targetKey).toBe("region:ap-northeast-1");
      onEvent({ type: "log", data: textLog, id: "cursor-1" });
      onEvent({ type: "log", data: cloudLog, id: "cursor-2" });
      return new Promise<string>((_resolve, reject) => {
        signal.addEventListener(
          "abort",
          () => reject(new DOMException("aborted", "AbortError")),
          { once: true },
        );
      });
    },
  );

  const view = render(
    <QueryClientProvider client={new QueryClient()}>
      <LocaleProvider>
        <ScanTaskEvents
          connectionID="con-23456789abcdefgh"
          scanID="scn-23456789abcdefgh"
          targetKey="region:ap-northeast-1"
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  await act(async () => Promise.resolve());

  const terminal = screen.getByRole("log");
  expect(screen.getByRole("heading", { level: 2, name: "Logs" })).toBeVisible();
  expect(terminal).toHaveTextContent(
    "2026-07-23 13:38:30,386 INFO [region:ap-northeast-1] Tokyo scan completed",
  );
  expect(terminal).toHaveClass("max-h-[32rem]", "overflow-auto");
  expect(terminal).not.toHaveTextContent("must-not-render");
  expect(terminal).toHaveTextContent(
    'resource-center SearchResources returned {"RequestId":"req-1","Resources":[{"ResourceId":"i-1"}]}',
  );
  expect(screen.getByText("Tokyo scan completed").parentElement).toHaveClass(
    "whitespace-pre-wrap",
    "break-words",
  );
  expect(terminal.querySelector("pre")).not.toBeInTheDocument();
  view.unmount();
});

it("keeps the terminal mounted and atomically replaces logs when the target changes", async () => {
  const tokyo = {
    id: "log-tokyo",
    job_id: "job-1",
    target_key: "region:ap-northeast-1",
    sequence: 1,
    kind: "text",
    level: "info",
    message: "Tokyo scan completed",
    created_at: "2026-07-23T13:38:30.386",
  } satisfies JobLog;
  const seoul = {
    ...tokyo,
    id: "log-seoul",
    target_key: "region:ap-northeast-2",
    message: "Seoul scan completed",
    created_at: "2026-07-23T13:38:31.004",
  } satisfies JobLog;
  let resolveSeoul: ((page: ScanLogPage) => void) | undefined;
  vi.mocked(getScanLogs)
    .mockResolvedValueOnce({
      items: [tokyo],
      live_cursor: "tokyo-live",
    })
    .mockImplementationOnce(
      async () =>
        new Promise((resolve) => {
          resolveSeoul = resolve;
        }),
    );
  vi.mocked(streamScanEvents).mockImplementation(
    async (_connection, _scan, _targetKey, _after, _onEvent, signal) =>
      new Promise<string>((_resolve, reject) => {
        signal.addEventListener(
          "abort",
          () => reject(new DOMException("aborted", "AbortError")),
          { once: true },
        );
      }),
  );
  const client = new QueryClient();
  const content = (targetKey: string) => (
    <QueryClientProvider client={client}>
      <LocaleProvider>
        <ScanTaskEvents
          connectionID="con-23456789abcdefgh"
          scanID="scn-23456789abcdefgh"
          targetKey={targetKey}
        />
      </LocaleProvider>
    </QueryClientProvider>
  );

  const view = render(content("region:ap-northeast-1"));
  await act(async () => Promise.resolve());
  const terminal = screen.getByRole("log");
  expect(screen.getByText("Tokyo scan completed")).toBeVisible();

  view.rerender(content("region:ap-northeast-2"));
  await act(async () => Promise.resolve());
  expect(screen.getByRole("log")).toBe(terminal);
  expect(screen.getByText("Tokyo scan completed")).toBeVisible();
  expect(screen.queryByText("Seoul scan completed")).not.toBeInTheDocument();

  await act(async () =>
    resolveSeoul?.({ items: [seoul], live_cursor: "seoul-live" }),
  );
  expect(screen.getByRole("log")).toBe(terminal);
  expect(screen.queryByText("Tokyo scan completed")).not.toBeInTheDocument();
  expect(screen.getByText("Seoul scan completed")).toBeVisible();
  expect(streamScanEvents).toHaveBeenLastCalledWith(
    "con-23456789abcdefgh",
    "scn-23456789abcdefgh",
    "region:ap-northeast-2",
    "seoul-live",
    expect.any(Function),
    expect.any(AbortSignal),
  );
  view.unmount();
});

it("auto-follows new logs only while the terminal remains near the bottom", async () => {
  let emit:
    | ((event: {
        type: "snapshot" | "log" | "end";
        data: JobLog;
        id?: string;
      }) => void)
    | undefined;
  vi.mocked(streamScanEvents).mockImplementation(
    async (_connection, _scan, _targetKey, _after, onEvent, signal) => {
      emit = onEvent as typeof emit;
      return new Promise<string>((_resolve, reject) => {
        signal.addEventListener(
          "abort",
          () => reject(new DOMException("aborted", "AbortError")),
          { once: true },
        );
      });
    },
  );
  const view = render(
    <QueryClientProvider client={new QueryClient()}>
      <LocaleProvider>
        <ScanTaskEvents
          connectionID="con-23456789abcdefgh"
          scanID="scn-23456789abcdefgh"
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  await act(async () => Promise.resolve());
  const terminal = screen.getByRole("log");
  const scrollTo = vi.fn();
  Object.defineProperties(terminal, {
    scrollHeight: { configurable: true, value: 300 },
    clientHeight: { configurable: true, value: 100 },
    scrollTop: { configurable: true, writable: true, value: 0 },
    scrollTo: { configurable: true, value: scrollTo },
  });

  fireEvent.scroll(terminal);
  await act(async () => {
    emit?.({
      type: "log",
      id: "cursor-1",
      data: {
        id: "log-1",
        job_id: "job-1",
        sequence: 1,
        kind: "text",
        level: "info",
        message: "first",
        created_at: "2026-07-23T13:38:30.386",
      },
    });
  });
  expect(scrollTo).not.toHaveBeenCalled();

  terminal.scrollTop = 190;
  fireEvent.scroll(terminal);
  await act(async () => {
    emit?.({
      type: "log",
      id: "cursor-2",
      data: {
        id: "log-2",
        job_id: "job-1",
        sequence: 2,
        kind: "text",
        level: "info",
        message: "second",
        created_at: "2026-07-23T13:38:31.386",
      },
    });
  });
  expect(scrollTo).toHaveBeenCalledWith({ top: 300 });
  view.unmount();
});

it("loads the latest history before streaming and prepends older logs without jumping", async () => {
  const duplicate = {
    id: "log-100",
    job_id: "job-1",
    sequence: 100,
    kind: "text",
    level: "info",
    message: "latest duplicate",
    created_at: "2026-07-23T13:38:30.100",
  } satisfies JobLog;
  const latest = {
    ...duplicate,
    id: "log-101",
    sequence: 101,
    message: "latest",
    created_at: "2026-07-23T13:38:30.101",
  } satisfies JobLog;
  const older = {
    ...duplicate,
    id: "log-099",
    sequence: 99,
    message: "older",
    created_at: "2026-07-23T13:38:30.099",
  } satisfies JobLog;
  let resolveOlder: ((page: ScanLogPage) => void) | undefined;
  vi.mocked(getScanLogs)
    .mockResolvedValueOnce({
      items: [duplicate, latest],
      next_cursor: "before-100",
      live_cursor: "after-101",
    })
    .mockImplementationOnce(
      async () =>
        new Promise((resolve) => {
          resolveOlder = resolve;
        }),
    );
  vi.mocked(streamScanEvents).mockImplementation(
    async (_connection, _scan, targetKey, after, _onEvent, signal) => {
      expect(targetKey).toBe("region:us-west-1");
      expect(after).toBe("after-101");
      return new Promise<string>((_resolve, reject) => {
        signal.addEventListener(
          "abort",
          () => reject(new DOMException("aborted", "AbortError")),
          { once: true },
        );
      });
    },
  );

  const view = render(
    <QueryClientProvider client={new QueryClient()}>
      <LocaleProvider>
        <ScanTaskEvents
          connectionID="con-23456789abcdefgh"
          scanID="scn-23456789abcdefgh"
          targetKey="region:us-west-1"
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  await act(async () => Promise.resolve());
  expect(getScanLogs).toHaveBeenNthCalledWith(
    1,
    "con-23456789abcdefgh",
    "scn-23456789abcdefgh",
    "region:us-west-1",
    "",
    expect.any(AbortSignal),
  );
  expect(streamScanEvents).toHaveBeenCalledTimes(1);
  expect(screen.getByText("latest")).toBeVisible();

  const terminal = screen.getByRole("log");
  let scrollHeight = 300;
  Object.defineProperties(terminal, {
    scrollHeight: { configurable: true, get: () => scrollHeight },
    scrollTop: { configurable: true, writable: true, value: 50 },
  });
  fireEvent.click(screen.getByRole("button", { name: "Load earlier logs" }));
  expect(getScanLogs).toHaveBeenNthCalledWith(
    2,
    "con-23456789abcdefgh",
    "scn-23456789abcdefgh",
    "region:us-west-1",
    "before-100",
    expect.any(AbortSignal),
  );
  scrollHeight = 500;
  await act(async () => resolveOlder?.({ items: [older, duplicate] }));

  expect(screen.getByText("older")).toBeVisible();
  expect(screen.getAllByText("latest duplicate")).toHaveLength(1);
  expect(terminal.scrollTop).toBe(250);
  expect(
    screen.queryByRole("button", { name: "Load earlier logs" }),
  ).not.toBeInTheDocument();
  view.unmount();
});
