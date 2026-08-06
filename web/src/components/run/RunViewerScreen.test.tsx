import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RunViewerScreen } from "./RunViewerScreen";
import { ApiError, api } from "@/lib/api-client";
import type { Run, RunStatus, Step, StepName } from "@/types/schema";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
}));

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

  it("polls while the run is live and stops once it reaches a terminal status", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let fetchCount = 0;
    const sequence: Array<{ status: RunStatus; steps: Step[] }> = [
      { status: "running", steps: [step("plan", { status: "complete" }), step("generate_sql", { status: "running" })] },
      { status: "running", steps: [step("plan", { status: "complete" }), step("generate_sql", { status: "complete" }), step("execute", { status: "running" })] },
      { status: "complete", steps: completeRunSteps() },
    ];
    getMock.mockImplementation(async () => {
      const item = sequence[Math.min(fetchCount, sequence.length - 1)];
      fetchCount += 1;
      return { run: run(item.status), steps: item.steps };
    });

    render(<RunViewerScreen workspaceId="ws-1" runId="run-1" />);
    await vi.waitFor(() =>
      expect(screen.getAllByText("Running").length).toBeGreaterThan(0),
    );

    await vi.advanceTimersByTimeAsync(1_000);
    await vi.advanceTimersByTimeAsync(1_000);
    await vi.waitFor(() => expect(screen.getByText(SQL_TEXT)).toBeInTheDocument());

    const stoppedAt = fetchCount;
    await vi.advanceTimersByTimeAsync(5_000);
    expect(fetchCount).toBe(stoppedAt);
  });
});