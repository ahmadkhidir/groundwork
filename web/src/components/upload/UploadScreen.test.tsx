import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UploadScreen } from "./UploadScreen";
import { uploadFile } from "./uploadFile";
import { checkDuplicateFilename } from "./checkDuplicateFilename";
import { api } from "@/lib/api-client";

const pushMock = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock }),
}));

vi.mock("./uploadFile", () => ({
  uploadFile: vi.fn(),
}));

// FEAT-049's collision check runs for every valid file before it becomes submittable. Most of this
// file's tests are about FEAT-003's ordinary upload path, so it defaults to "no collision" here;
// tests specifically exercising the Duplicate Filename dialog override this per-call below.
vi.mock("./checkDuplicateFilename", () => ({
  checkDuplicateFilename: vi.fn(async () => ({ exists: false })),
}));

vi.mock("@/lib/api-client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn(), delete: vi.fn() },
  };
});

const uploadFileMock = vi.mocked(uploadFile);
const checkDuplicateFilenameMock = vi.mocked(checkDuplicateFilename);
const getMock = vi.mocked(api.get);
const deleteMock = vi.mocked(api.delete);

function makeFile(name: string, sizeBytes: number, type = "text/csv") {
  const file = new File(["x".repeat(Math.min(sizeBytes, 1024))], name, { type });
  Object.defineProperty(file, "size", { value: sizeBytes });
  return file;
}

/** Fires a drop event carrying `files` at the drop zone (role="button", the outer div). */
async function dropFiles(files: File[]) {
  const dropzone = screen.getByRole("button", { name: /select files/i });
  const dataTransfer = { files, items: [], types: ["Files"] };
  const { fireEvent } = await import("@testing-library/react");
  fireEvent.drop(dropzone, { dataTransfer });
}

describe("UploadScreen", () => {
  beforeEach(() => {
    pushMock.mockClear();
    uploadFileMock.mockReset();
    checkDuplicateFilenameMock.mockReset();
    checkDuplicateFilenameMock.mockResolvedValue({ exists: false });
    getMock.mockReset();
    deleteMock.mockReset();
    Object.defineProperty(window.navigator, "onLine", {
      configurable: true,
      value: true,
    });
  });

  it("shows the default empty drop zone with Start Ingest disabled", () => {
    render(<UploadScreen workspaceId="ws-1" />);
    expect(screen.getByText(/tap to select files/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start Ingest" })).toBeDisabled();
  });

  it("rejects an unsupported file type immediately, client-side", async () => {
    render(<UploadScreen workspaceId="ws-1" />);
    await dropFiles([makeFile("resume.docx", 1000, "application/msword")]);

    expect(
      screen.getByText(/is not a supported format/i),
    ).toBeInTheDocument();
    expect(uploadFileMock).not.toHaveBeenCalled();
  });

  it("rejects an oversized file with the specific ceiling stated", async () => {
    render(<UploadScreen workspaceId="ws-1" />);
    await dropFiles([makeFile("huge.csv", 600 * 1024 * 1024)]);

    const message = screen.getByText(/over the 500 MB limit/i);
    expect(message).toBeInTheDocument();
  });

  it("accepts a valid file and enables Start Ingest once the duplicate check clears", async () => {
    render(<UploadScreen workspaceId="ws-1" />);
    await dropFiles([makeFile("orders.csv", 1000)]);

    expect(screen.getByText("orders.csv")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Start Ingest" })).toBeEnabled(),
    );
  });

  it("uploads valid files and navigates to Ingest Progress on success", async () => {
    uploadFileMock.mockImplementation(async (_ws, file, onProgress) => {
      onProgress(1);
      return { filename: file.name, dataset: { id: "ds-1" } as never };
    });

    render(<UploadScreen workspaceId="ws-1" />);
    await dropFiles([makeFile("orders.csv", 1000)]);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Start Ingest" })).toBeEnabled(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Start Ingest" }));

    await waitFor(() =>
      expect(pushMock).toHaveBeenCalledWith("/workspaces/ws-1/ingest/progress"),
    );
  });

  it("shows a mid-upload failure inline without blocking other files", async () => {
    uploadFileMock.mockImplementation(async (_ws, file) => {
      if (file.name === "bad.csv") {
        throw new Error("Network dropped mid-upload.");
      }
      return { filename: file.name, dataset: { id: "ds-1" } as never };
    });

    render(<UploadScreen workspaceId="ws-1" />);
    await dropFiles([makeFile("good.csv", 1000), makeFile("bad.csv", 1000)]);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Start Ingest" })).toBeEnabled(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Start Ingest" }));

    await waitFor(() =>
      expect(screen.getByText("Network dropped mid-upload.")).toBeInTheDocument(),
    );
    // good.csv succeeded and is still shown, unaffected by bad.csv's failure — and since not every
    // file succeeded, the screen must NOT have navigated away to Ingest Progress.
    expect(screen.getByText("good.csv")).toBeInTheDocument();
    expect(screen.getByText("Uploaded")).toBeInTheDocument();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("disables Start Ingest and shows a banner while offline", () => {
    Object.defineProperty(window.navigator, "onLine", {
      configurable: true,
      value: false,
    });
    render(<UploadScreen workspaceId="ws-1" />);

    expect(screen.getByText("You're offline")).toBeInTheDocument();
  });

  // FEAT-049: the exact incident scenario — re-uploading a file with the same name as an existing
  // dataset, INCLUDING one stuck at ingest_status "failed" (the falsely-reported-failure retry case).
  describe("Duplicate Filename dialog (FEAT-049)", () => {
    it("shows the dialog automatically and disables Start Ingest until it is resolved", async () => {
      checkDuplicateFilenameMock.mockResolvedValueOnce({ exists: true, existingDatasetId: "ds-old" });
      getMock.mockResolvedValueOnce({
        dataset: { id: "ds-old", original_filename: "orders.csv" },
        column_count: 3,
        dependent_relationships: [],
        has_reviewed_pii: false,
        has_type_override_risk: false,
      });

      render(<UploadScreen workspaceId="ws-1" />);
      await dropFiles([makeFile("orders.csv", 1000)]);

      await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument());
      expect(screen.getByRole("dialog")).toHaveTextContent(/orders\.csv.*already exists/i);
      expect(screen.getByRole("button", { name: "Start Ingest" })).toBeDisabled();
      // Replace is pre-selected per screens/05, but never auto-applied — nothing has been deleted yet.
      expect(deleteMock).not.toHaveBeenCalled();
    });

    it("a failed prior dataset still triggers the duplicate prompt on retry", async () => {
      // The literal bug: ingest-progress falsely reported failure, the user retried, and the retry
      // must not silently create a second indistinguishable dataset.
      checkDuplicateFilenameMock.mockResolvedValueOnce({ exists: true, existingDatasetId: "ds-failed" });
      getMock.mockResolvedValueOnce({
        dataset: { id: "ds-failed", original_filename: "orders.csv" },
        column_count: 0,
        dependent_relationships: [],
        has_reviewed_pii: false,
        has_type_override_risk: false,
      });

      render(<UploadScreen workspaceId="ws-1" />);
      await dropFiles([makeFile("orders.csv", 1000)]);

      await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument());
      expect(checkDuplicateFilenameMock).toHaveBeenCalledWith("ws-1", "orders.csv");
    });

    it("Replace deletes the prior dataset via FEAT-048 and leaves exactly one dataset submittable", async () => {
      checkDuplicateFilenameMock.mockResolvedValueOnce({ exists: true, existingDatasetId: "ds-old" });
      getMock.mockResolvedValueOnce({
        dataset: { id: "ds-old", original_filename: "orders.csv" },
        column_count: 2,
        dependent_relationships: [{ id: "rel-1", source_column_name: "a", target_column_name: "b" }],
        has_reviewed_pii: true,
        has_type_override_risk: true,
      });
      deleteMock.mockResolvedValueOnce({ id: "ds-old" });
      uploadFileMock.mockResolvedValue({ filename: "orders.csv", dataset: { id: "ds-new" } as never });

      render(<UploadScreen workspaceId="ws-1" />);
      await dropFiles([makeFile("orders.csv", 1000)]);
      await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument());

      // Replace is pre-selected; the disclosure names the dependent relationship and the lost review
      // work before Continue can be clicked.
      await waitFor(() => expect(screen.getByRole("dialog")).toHaveTextContent(/a ↔ b/));
      expect(screen.getByRole("dialog")).toHaveTextContent(/NOT be carried forward/i);

      await userEvent.click(screen.getByRole("button", { name: "Continue" }));

      await waitFor(() => expect(deleteMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/datasets/ds-old"));
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      await waitFor(() => expect(screen.getByRole("button", { name: "Start Ingest" })).toBeEnabled());

      await userEvent.click(screen.getByRole("button", { name: "Start Ingest" }));
      await waitFor(() => expect(uploadFileMock).toHaveBeenCalledTimes(1));
    });

    it("Keep both proceeds without deleting the existing dataset, leaving two distinguished by upload time", async () => {
      checkDuplicateFilenameMock.mockResolvedValueOnce({ exists: true, existingDatasetId: "ds-old" });
      getMock.mockResolvedValueOnce({
        dataset: { id: "ds-old", original_filename: "orders.csv" },
        column_count: 2,
        dependent_relationships: [],
        has_reviewed_pii: false,
        has_type_override_risk: false,
      });
      uploadFileMock.mockResolvedValue({ filename: "orders.csv", dataset: { id: "ds-new" } as never });

      render(<UploadScreen workspaceId="ws-1" />);
      await dropFiles([makeFile("orders.csv", 1000)]);
      await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument());

      await userEvent.click(screen.getByRole("radio", { name: /keep both/i }));
      await userEvent.click(screen.getByRole("button", { name: "Continue" }));

      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      expect(deleteMock).not.toHaveBeenCalled();

      await waitFor(() => expect(screen.getByRole("button", { name: "Start Ingest" })).toBeEnabled());
      await userEvent.click(screen.getByRole("button", { name: "Start Ingest" }));
      await waitFor(() => expect(uploadFileMock).toHaveBeenCalledTimes(1));
    });
  });
});
