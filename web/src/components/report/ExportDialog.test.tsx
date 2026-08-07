import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ExportDialog, downloadExportFile } from "./ExportDialog";
import { SubscriptionTier } from "@/types/schema";

// A reactive URLSearchParams store so tests can simulate real navigation: when `router.replace`
// drops the export param (or a test sets a fresh param set), consumers re-render — exactly like
// Next's useSearchParams does after a navigation.
const { replaceMock, searchParamsStore } = vi.hoisted(() => {
  let value = new URLSearchParams();
  const listeners = new Set<() => void>();
  const store = {
    get: () => value,
    set: (next: URLSearchParams) => {
      value = next;
      listeners.forEach((listener) => listener());
    },
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
  return {
    replaceMock: vi.fn((url: string) => {
      const q = url.includes("?") ? url.slice(url.indexOf("?") + 1) : "";
      store.set(new URLSearchParams(q));
    }),
    searchParamsStore: store,
  };
});
vi.mock("next/navigation", async () => {
  const React = await import("react");
  return {
    useRouter: () => ({ push: vi.fn(), replace: replaceMock }),
    useSearchParams: () =>
      React.useSyncExternalStore(searchParamsStore.subscribe, searchParamsStore.get),
  };
});

const fetchMock = vi.fn();
let capturedAnchor: HTMLAnchorElement | null = null;

/** Captures the temporary download anchor the dialog creates, so tests can assert its filename. */
function captureDownloadAnchor() {
  const originalCreateElement = document.createElement.bind(document);
  vi.spyOn(document, "createElement").mockImplementation(
    (tagName: string, options?: ElementCreationOptions) => {
      const element = originalCreateElement(tagName, options);
      if (tagName.toLowerCase() === "a") {
        capturedAnchor = element as HTMLAnchorElement;
      }
      return element;
    },
  );
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
}

function renderDialog(props?: Partial<React.ComponentProps<typeof ExportDialog>>) {
  return render(
    <ExportDialog workspaceId="ws-1" reportId="report-1" {...props} />,
  );
}

/** A minimal fetch Response-shaped object; jsdom lacks a full Response implementation. */
function okResponse(filename?: string) {
  return {
    ok: true,
    status: 200,
    headers: {
      get: (name: string) =>
        name === "content-disposition" && filename
          ? `attachment; filename="${filename}"`
          : null,
    },
    blob: async () => new Blob(["file-bytes"], { type: "application/octet-stream" }),
  };
}

function forbiddenResponse() {
  return {
    ok: false,
    status: 403,
    headers: { get: () => null },
    json: async () => ({
      error: "Excel/CSV export is available on the Pro and Team plans. Upgrade to export the underlying result set.",
      code: "upgrade_required",
    }),
  };
}

describe("ExportDialog", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    replaceMock.mockClear();
    searchParamsStore.set(new URLSearchParams("export=1"));
    capturedAnchor = null;

    vi.stubGlobal("fetch", fetchMock);
    // jsdom has no object-URL plumbing; stub it and capture the anchor the dialog clicks.
    URL.createObjectURL = vi.fn(() => "blob:mock");
    URL.revokeObjectURL = vi.fn();
    captureDownloadAnchor();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("renders the format rows and keeps Export disabled until a format is selected", () => {
    // Pro tier so both implemented formats are selectable radios; the locked-row rendering is
    // covered by the Free-tier test below.
    renderDialog({ tier: SubscriptionTier.Pro });
    expect(screen.getByRole("heading", { name: "Export report" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /PDF/ })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Excel\/CSV/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeDisabled();
  });

  it("enables Export once an available format is selected", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(screen.getByRole("radio", { name: /PDF/ }));
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();
  });

  it("keeps the Excel/CSV row visible but not selectable for a Free user (never hidden)", async () => {
    const user = userEvent.setup();
    renderDialog({ tier: SubscriptionTier.Free });

    // The locked row is NOT a radio: it renders the Upgrade Prompt and cannot be selected.
    const csvRow = screen.getByText("Excel/CSV").closest("div[aria-disabled='true']");
    expect(csvRow).not.toBeNull();
    const upgradeLink = within(csvRow as HTMLElement).getByRole("link", {
      name: /Upgrade to Pro\/Team/,
    });
    expect(upgradeLink).toHaveAttribute("href", "/settings");

    // Clicking the row itself must do nothing: Export stays disabled.
    await user.click(screen.getByText("Excel/CSV"));
    expect(screen.getByRole("button", { name: "Export" })).toBeDisabled();
    // No format radio is checked.
    expect(screen.queryByRole("radio", { name: /Excel\/CSV/ })).not.toBeInTheDocument();
  });

  it("lets a Pro user select Excel/CSV and exports the result set as CSV", async () => {
    const user = userEvent.setup();
    fetchMock.mockResolvedValue(okResponse("customers-results.csv"));
    renderDialog({ tier: SubscriptionTier.Pro });

    await user.click(screen.getByRole("radio", { name: /Excel\/CSV/ }));
    await user.click(screen.getByRole("button", { name: "Export" }));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/v1/workspaces/ws-1/reports/report-1/export?format=csv"),
        expect.objectContaining({ credentials: "include" }),
      ),
    );
    await waitFor(() =>
      expect(screen.getByRole("status")).toHaveTextContent("Export ready"),
    );
    // The server-provided Content-Disposition filename wins.
    expect(capturedAnchor?.download).toBe("customers-results.csv");
  });

  it("shows the exporting state on the selected row while the export is in flight", async () => {
    const user = userEvent.setup();
    fetchMock.mockImplementation(() => new Promise(() => {})); // never resolves
    renderDialog();

    await user.click(screen.getByRole("radio", { name: /PDF/ }));
    await user.click(screen.getByRole("button", { name: "Export" }));

    expect(await screen.findByText("Preparing your export…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeDisabled();
  });

  it("shows an inline error with Try Again and stays open when the export fails", async () => {
    const user = userEvent.setup();
    fetchMock.mockResolvedValueOnce(forbiddenResponse());
    renderDialog({ tier: SubscriptionTier.Pro });

    await user.click(screen.getByRole("radio", { name: /Excel\/CSV/ }));
    await user.click(screen.getByRole("button", { name: "Export" }));

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Excel/CSV export is available on the Pro and Team plans.",
      ),
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    // Retry re-runs the export (fetch called again).
    fetchMock.mockResolvedValueOnce(okResponse());
    await user.click(screen.getByRole("button", { name: "Try Again" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(screen.getByRole("status")).toHaveTextContent("Export ready"),
    );
  });

  it("closing the dialog during an export does not cancel the job (screens/11 edge case)", async () => {
    const user = userEvent.setup();
    let resolveFetch: (value: unknown) => void = () => {};
    fetchMock.mockImplementation(
      () => new Promise((resolve) => (resolveFetch = resolve)),
    );
    renderDialog();

    await user.click(screen.getByRole("radio", { name: /PDF/ }));
    await user.click(screen.getByRole("button", { name: "Export" }));
    expect(screen.getByRole("button", { name: "Export" })).toBeDisabled();

    // The mocked router already drops the param on replace, so the dialog unmounts like the real
    // one would after navigation.
    await user.click(screen.getByRole("button", { name: "Close export dialog" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    // The queued export still completes and the download still fires.
    resolveFetch(okResponse("late.pdf"));
    await waitFor(() => expect(capturedAnchor?.download).toBe("late.pdf"));
  });

  it("closing drops only the export param, leaving the report URL intact", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(screen.getByRole("button", { name: "Close export dialog" }));
    expect(replaceMock).toHaveBeenCalledWith(
      expect.not.stringContaining("export"),
    );
  });

  it("describes a narrative-only report honestly (no chart) and omits the Image row (screens/11 edge case)", () => {
    // Pro tier so the gated rows are radios and the row count is unambiguous.
    renderDialog({ hasChart: false, tier: SubscriptionTier.Pro });
    expect(screen.getByText(/This report has no chart/)).toBeInTheDocument();
    // screens/11: the Image-export row is omitted entirely for narrative-only reports — never shown
    // and then failing. The remaining rows (PDF, Excel/CSV, Markdown/HTML, JSON) all stay; the
    // Team-only PowerPoint row is a locked div for a Pro user.
    expect(screen.queryByText("Chart image")).not.toBeInTheDocument();
    expect(screen.getAllByRole("radio")).toHaveLength(4);
  });

  it("shows all six format rows for a Pro user with a chart — PowerPoint locked, never hidden", () => {
    renderDialog({ tier: SubscriptionTier.Pro, hasChart: true });
    expect(screen.getByRole("radio", { name: /PDF/ })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Excel\/CSV/ })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Markdown\/HTML/ })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /JSON/ })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Chart image/ })).toBeInTheDocument();

    // PowerPoint is Team-only (monetization.md row 120): for Pro it renders as a locked row with
    // the Upgrade Prompt, exactly like the Free-tier treatment of the other paid rows.
    const pptxRow = screen.getByText("PowerPoint").closest("div[aria-disabled='true']");
    expect(pptxRow).not.toBeNull();
    const upgradeLink = within(pptxRow as HTMLElement).getByRole("link", {
      name: /Upgrade to Pro\/Team/,
    });
    expect(upgradeLink).toHaveAttribute("href", "/settings");
    expect(screen.getAllByRole("radio")).toHaveLength(5);
  });

  it("keeps every paid format row visible but not selectable for a Free user (never hidden)", () => {
    renderDialog({ tier: SubscriptionTier.Free, hasChart: true });
    for (const name of ["Excel/CSV", "PowerPoint", "Markdown/HTML", "JSON", "Chart image"]) {
      const row = screen.getByText(name).closest("div[aria-disabled='true']");
      expect(row).not.toBeNull();
    }
    // Only PDF is a selectable radio for a Free user; the rest are locked rows.
    expect(screen.getAllByRole("radio")).toHaveLength(1);
  });

  it("lets a Team user select PowerPoint and export the deck", async () => {
    const user = userEvent.setup();
    fetchMock.mockResolvedValue(okResponse("customers.pptx"));
    renderDialog({ tier: SubscriptionTier.Team });

    await user.click(screen.getByRole("radio", { name: /PowerPoint/ }));
    await user.click(screen.getByRole("button", { name: "Export" }));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/v1/workspaces/ws-1/reports/report-1/export?format=pptx"),
        expect.objectContaining({ credentials: "include" }),
      ),
    );
    await waitFor(() =>
      expect(screen.getByRole("status")).toHaveTextContent("Export ready"),
    );
    expect(capturedAnchor?.download).toBe("customers.pptx");
  });

  it("exports Markdown by default from the Markdown/HTML row, with the variant picker shown", async () => {
    const user = userEvent.setup();
    fetchMock.mockResolvedValue(okResponse("report.md"));
    renderDialog({ tier: SubscriptionTier.Pro });

    await user.click(screen.getByRole("radio", { name: /Markdown\/HTML/ }));
    // The inline variant picker appears only while the Markdown/HTML row is selected, defaulting
    // to Markdown (the two server formats behind the one dialog row).
    const variant = screen.getByRole("combobox", { name: "Markdown or HTML variant" });
    expect(variant).toHaveValue("markdown");
    await user.click(screen.getByRole("button", { name: "Export" }));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("?format=markdown"),
        expect.anything(),
      ),
    );
    expect(capturedAnchor?.download).toBe("report.md");
  });

  it("switching the Markdown/HTML variant to HTML exports format=html", async () => {
    const user = userEvent.setup();
    fetchMock.mockResolvedValue(okResponse("report.html"));
    renderDialog({ tier: SubscriptionTier.Pro });

    await user.click(screen.getByRole("radio", { name: /Markdown\/HTML/ }));
    await user.selectOptions(
      screen.getByRole("combobox", { name: "Markdown or HTML variant" }),
      "html",
    );
    await user.click(screen.getByRole("button", { name: "Export" }));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("?format=html"),
        expect.anything(),
      ),
    );
    expect(capturedAnchor?.download).toBe("report.html");
  });

  it("lets a Pro user export the report as JSON", async () => {
    const user = userEvent.setup();
    fetchMock.mockResolvedValue(okResponse("report.json"));
    renderDialog({ tier: SubscriptionTier.Pro });

    await user.click(screen.getByRole("radio", { name: /JSON/ }));
    await user.click(screen.getByRole("button", { name: "Export" }));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("?format=json"),
        expect.anything(),
      ),
    );
    expect(capturedAnchor?.download).toBe("report.json");
  });

  it("lets a Pro user export the chart as a standalone image", async () => {
    const user = userEvent.setup();
    fetchMock.mockResolvedValue(okResponse("report-chart.svg"));
    renderDialog({ tier: SubscriptionTier.Pro });

    await user.click(screen.getByRole("radio", { name: /Chart image/ }));
    await user.click(screen.getByRole("button", { name: "Export" }));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("?format=image"),
        expect.anything(),
      ),
    );
    expect(capturedAnchor?.download).toBe("report-chart.svg");
  });
});

describe("downloadExportFile", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
    URL.createObjectURL = vi.fn(() => "blob:mock");
    URL.revokeObjectURL = vi.fn();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("throws an ApiError shaped like the api-client on a non-2xx response", async () => {
    fetchMock.mockResolvedValue(forbiddenResponse());
    await expect(
      downloadExportFile("/v1/workspaces/ws-1/reports/report-1/export?format=csv", "x.csv"),
    ).rejects.toMatchObject({ status: 403, code: "upgrade_required" });
  });

  it("triggers a download and revokes the object URL", async () => {
    fetchMock.mockResolvedValue(okResponse("report.pdf"));
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    await downloadExportFile(
      "/v1/workspaces/ws-1/reports/report-1/export?format=pdf",
      "groundwork-report.pdf",
    );
    expect(URL.createObjectURL).toHaveBeenCalled();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:mock");
  });
});
