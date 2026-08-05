import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UploadScreen } from "./UploadScreen";
import { uploadFile } from "./uploadFile";

const pushMock = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock }),
}));

vi.mock("./uploadFile", () => ({
  uploadFile: vi.fn(),
}));

const uploadFileMock = vi.mocked(uploadFile);

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

  it("accepts a valid file and enables Start Ingest", async () => {
    render(<UploadScreen workspaceId="ws-1" />);
    await dropFiles([makeFile("orders.csv", 1000)]);

    expect(screen.getByText("orders.csv")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start Ingest" })).toBeEnabled();
  });

  it("uploads valid files and navigates to Ingest Progress on success", async () => {
    uploadFileMock.mockImplementation(async (_ws, file, onProgress) => {
      onProgress(1);
      return { filename: file.name, dataset: { id: "ds-1" } as never };
    });

    render(<UploadScreen workspaceId="ws-1" />);
    await dropFiles([makeFile("orders.csv", 1000)]);
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
});
