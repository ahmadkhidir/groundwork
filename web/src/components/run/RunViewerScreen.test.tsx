import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RunViewerScreen } from "./RunViewerScreen";
import { ApiError, api } from "@/lib/api-client";
import type { Run, RunStatus, Step, StepName } from "@/types/schema";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn(), post: vi.fn() },
  };
});

const { pushMock, routerMock } = vi.hoisted(() => {
  const pushMock = vi.fn();
  return { pushMock, routerMock: { push: pushMock } };
});
vi.mock("next/navigation", () => ({
  useRouter: () => routerMock,
}));

// FEAT-023: the viewer subscribes to an SSE stream (EventSource). jsdom has no EventSource, so a
// minimal mock stands in — it records instances and lets tests push a message at one.
class MockEventSource {
  static instances: MockEventSource[] = [];
  readonly url: string;
  onopen: ((ev: Event) => unknown) | null = null;
  onmessage: ((ev: MessageEvent) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  close = vi.fn();

  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }
}
vi.stubGlobal("EventSource", MockEventSource);

function stream(index = 0): MockEventSource {
  return MockEventSource.instances[index];
}

function emit(index: number, data: string): void {
  const source = MockEventSource.instances[index];
  source.onmessage?.({ data } as unknown as MessageEvent);
}

const getMock = vi.mocked(api.get);
const postMock = vi.mocked(api.post);

function step(
  stepName: StepName,
  overrides: Partial<Omit<Step, "step_name" | "idempotency_key">> = {},
): Step {
  return {
    id: `step-${stepName}`,
    run_id: "run-1",
    step_name: stepName,
    status: "complete",
    idempotency_key: `run-1:${stepName}`,
    started_at: "2026-01-01T00:00:00Z",
    completed_at: "2026-01-01T00:00:02Z",
    ...overrides,
  };
}

function run(status: RunStatus, overrides: Partial<Run> = {}): Run {
  return {
    id: "run-1",
    workspace_id: "ws-1",
    question_text: "How many customers per region?",
    status,
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

const SQL_TEXT =
  "SELECT region, COUNT(*) AS cnt FROM dataset_sales_q3_d5f3e2a1 GROUP BY region";

function completeRunSteps(): Step[] {
  return [
    step("plan", { output: JSON.stringify({ answerable: true, approach: "group by region" }) }),
    step("generate_sql", { output: JSON.stringify({ sql: SQL_TEXT }) }),
    step("execute", {
      output: JSON.stringify({ columns: ["region", "cnt"], rows: [["North", 10]], row_count: 1 }),
    }),
  ];
}

describe("RunViewerScreen", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
    pushMock.mockReset();
    MockEventSource.instances.length = 0;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders the run, status pill, step feed, generated SQL, and result facts once loaded", async () => {
    getMock.mockResolvedValue({ run: run("complete"), steps: completeRunSteps() });
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);

    await screen.findByText("How many customers per region?");
    // The run pill renders as a span; each step row's status renders as a p.
    expect(screen.getByText("Complete", { selector: "span" })).toBeInTheDocument();
    expect(screen.getAllByText("Complete", { selector: "p" })).toHaveLength(3);
    expect(screen.getByText("Plan")).toBeInTheDocument();
    expect(screen.getByText("Generate SQL")).toBeInTheDocument();
    expect(screen.getByText("Execute")).toBeInTheDocument();
    expect(screen.getByText(SQL_TEXT)).toBeInTheDocument();
    // Result facts from the execute step.
    expect(screen.getByText("North")).toBeInTheDocument();
    expect(screen.getByText("10")).toBeInTheDocument();
    expect(screen.getByText("1 row")).toBeInTheDocument();
  });

  it("shows the waking notice while the engine is waking, before steps begin", async () => {
    getMock.mockResolvedValue({ run: run("waking"), steps: [] });
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);

    await screen.findByText(/Waking engine/);
    expect(screen.getByText("Waiting for the run to begin…")).toBeInTheDocument();
  });

  it("surfaces the Phase 2 clarification reason when the plan judges the question unanswerable", async () => {
    getMock.mockResolvedValue({
      run: run("failed"),
      steps: [
        step("plan", {
          output: JSON.stringify({
            answerable: false,
            reason: "The data has no column that records region.",
          }),
        }),
      ],
    });
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("This question needs clarification");
    expect(alert).toHaveTextContent("The data has no column that records region.");
  });

  it("shows a failed step's reason in the feed", async () => {
    getMock.mockResolvedValue({
      run: run("failed"),
      steps: [
        step("plan", { output: JSON.stringify({ answerable: true, approach: "x" }) }),
        step("generate_sql", { status: "failed", output: "The model returned no SQL." }),
      ],
    });
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);

    await screen.findByText("The model returned no SQL.");
    // Run pill shows Failed (span) and the generate_sql row shows Failed (p).
    expect(screen.getByText("Failed", { selector: "span" })).toBeInTheDocument();
    expect(screen.getAllByText("Failed", { selector: "p" })).toHaveLength(1);
    // No clarification banner when the plan DID answer the question.
    expect(screen.queryByText("This question needs clarification")).not.toBeInTheDocument();
  });

  it("shows the error state with a working retry when the initial fetch fails", async () => {
    getMock.mockRejectedValueOnce(new ApiError(500, "Server exploded"));
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);

    await screen.findByText("Server exploded");
    getMock.mockResolvedValueOnce({ run: run("complete"), steps: completeRunSteps() });
    screen.getByRole("button", { name: "Try Again" }).click();

    await screen.findByText("How many customers per region?");
  });

  it("streams live DAG transitions and stops at a terminal status (FEAT-023)", async () => {
    getMock.mockResolvedValue({
      run: run("running"),
      steps: [step("plan", { status: "complete" }), step("generate_sql", { status: "running" })],
    });
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);
    await screen.findByText("How many customers per region?");

    // The viewer opens an SSE stream for the run.
    const source = stream(0);
    expect(source.url).toContain("/v1/workspaces/ws-1/runs/run-1/stream");

    // generate_sql completes over the stream with its output.
    emit(
      0,
      JSON.stringify({
        type: "step.completed",
        step_name: "generate_sql",
        step_status: "complete",
        output: JSON.stringify({ sql: SQL_TEXT }),
      }),
    );
    await screen.findByText(SQL_TEXT);

    // A step absent from the initial snapshot is added live.
    emit(
      0,
      JSON.stringify({ type: "step.started", step_name: "execute", step_status: "running" }),
    );
    await waitFor(() => expect(screen.getAllByText("Running").length).toBeGreaterThan(0));
    expect(screen.getByText("Execute")).toBeInTheDocument();

    // run.status complete ends the live run; without a report_id the viewer stays put (no navigation).
    emit(0, JSON.stringify({ type: "run.status", status: "complete" }));
    await waitFor(() => expect(pushMock).not.toHaveBeenCalled());
  });

  it("surfaces a determinism-cache hit with the cached-from-run reference (FEAT-027)", async () => {
    getMock.mockResolvedValue({ run: run("running"), steps: [] });
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);
    await screen.findByText("How many customers per region?");

    emit(0, JSON.stringify({ type: "cache.hit", cached_run_id: "run-9" }));

    expect(
      await screen.findByText(/answered instantly from a prior identical run/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/cached from run run-9/i)).toBeInTheDocument();
  });

  it("auto-navigates to the report on completion when a report_id is attached (FEAT-018)", async () => {
    getMock.mockResolvedValue({
      run: run("complete"),
      steps: completeRunSteps(),
      report_id: "report-1",
    });
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);

    await screen.findByText("How many customers per region?");
    expect(pushMock).toHaveBeenCalledWith("/workspaces/ws-1/reports/report-1");
  });

  it("does not navigate away when a completed run has no report yet", async () => {
    getMock.mockResolvedValue({ run: run("complete"), steps: completeRunSteps() });
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);

    await screen.findByText("How many customers per region?");
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("starts a NEW run seeded with the edited SQL and navigates to it (FEAT-018)", async () => {
    getMock.mockResolvedValue({ run: run("complete"), steps: completeRunSteps() });
    postMock.mockResolvedValue({ run: run("complete", { id: "run-2" }) });
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);

    await screen.findByText(SQL_TEXT);
    screen.getByRole("button", { name: "Edit SQL" }).click();

    const textarea = await screen.findByRole("textbox", { name: "Edit Generated SQL" });
    const edited = "SELECT region FROM dataset_sales_q3_d5f3e2a1 WHERE cnt > 5";
    // fireEvent is safer than userEvent for a controlled textarea under fake timers.
    const { fireEvent } = await import("@testing-library/react");
    fireEvent.change(textarea, { target: { value: edited } });

    screen.getByRole("button", { name: "Run edited query" }).click();

    await vi.waitFor(() => {
      expect(postMock).toHaveBeenCalledWith(
        "/v1/workspaces/ws-1/runs",
        expect.objectContaining({ question_text: "How many customers per region?", sql: edited }),
      );
      expect(pushMock).toHaveBeenCalledWith("/workspaces/ws-1/runs/run-2");
    });
  });

  it("links to the semantic-layer Corrections tab for an edited-SQL run whose correction was captured (FEAT-020)", async () => {
    // A seeded run (FEAT-018) has no plan step: the user's edited SQL was captured as a durable
    // correction, and screen 15's entry point says the Run Viewer links to it.
    getMock.mockResolvedValue({
      run: run("complete"),
      steps: [
        step("generate_sql", { output: JSON.stringify({ sql: SQL_TEXT }) }),
        step("execute", {
          output: JSON.stringify({ columns: ["region", "cnt"], rows: [["North", 10]], row_count: 1 }),
        }),
      ],
    });
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);

    const link = await screen.findByRole("link", { name: /correction saved/i });
    expect(link).toHaveAttribute("href", "/workspaces/ws-1/semantic-layer");
  });

  it("does not show the correction-saved link for a normal run that had a plan step", async () => {
    getMock.mockResolvedValue({ run: run("complete"), steps: completeRunSteps() });
    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);

    await screen.findByText("How many customers per region?");
    expect(screen.queryByRole("link", { name: /correction saved/i })).not.toBeInTheDocument();
  });
});