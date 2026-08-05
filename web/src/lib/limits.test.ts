import { describe, expect, it } from "vitest";
import {
  MAX_UPLOAD_BYTES,
  formatBytes,
  maxUploadSizeLabel,
  validateUploadFile,
} from "./limits";

describe("upload size ceiling", () => {
  it("is 500 MB, the confirmed product decision", () => {
    expect(MAX_UPLOAD_BYTES).toBe(500 * 1024 * 1024);
    expect(maxUploadSizeLabel()).toBe("500 MB");
  });

  it("accepts a supported file at exactly the ceiling", () => {
    // Boundary: the limit is inclusive. A file of exactly 500 MB is allowed.
    const result = validateUploadFile({
      name: "ledger.csv",
      size: MAX_UPLOAD_BYTES,
    });
    expect(result.valid).toBe(true);
    expect(result.message).toBe("");
  });

  it("rejects a file one byte over the ceiling", () => {
    const result = validateUploadFile({
      name: "ledger.csv",
      size: MAX_UPLOAD_BYTES + 1,
    });
    expect(result.valid).toBe(false);
  });

  /*
   * screens/05-file-upload.md is explicit: an oversized file is "rejected with the specific ceiling
   * stated, not a generic 'file too large'". This asserts that requirement directly, because it is
   * the kind of copy that quietly degrades into a generic message during later refactoring.
   */
  it("states the specific ceiling and the file's own size in the rejection", () => {
    const result = validateUploadFile({
      name: "huge.csv",
      size: 750 * 1024 * 1024,
    });

    expect(result.valid).toBe(false);
    expect(result.message).toContain("500 MB");
    expect(result.message).toContain("750 MB");
    expect(result.message).toContain("huge.csv");
    expect(result.message.toLowerCase()).not.toBe("file too large");
  });

  it("rejects an unsupported format before considering size", () => {
    const result = validateUploadFile({ name: "notes.pdf", size: 1024 });
    expect(result.valid).toBe(false);
    expect(result.message).toContain("not a supported format");
  });

  it.each([
    ["csv", "data.csv"],
    ["excel", "data.xlsx"],
    ["parquet", "data.parquet"],
    ["sqlite", "data.sqlite"],
  ])("accepts the %s format from schema.md's DatasetFormat enum", (_, name) => {
    expect(validateUploadFile({ name, size: 2048 }).valid).toBe(true);
  });
});

describe("formatBytes", () => {
  it.each([
    [0, "0 B"],
    [512, "512 B"],
    [1024, "1.0 KB"],
    [1024 * 1024, "1.0 MB"],
    [500 * 1024 * 1024, "500 MB"],
    [1024 * 1024 * 1024, "1.0 GB"],
  ])("renders %i bytes as %s", (bytes, expected) => {
    expect(formatBytes(bytes)).toBe(expected);
  });

  it("renders a placeholder rather than NaN for an invalid size", () => {
    expect(formatBytes(Number.NaN)).toBe("—");
    expect(formatBytes(-1)).toBe("—");
  });
});
