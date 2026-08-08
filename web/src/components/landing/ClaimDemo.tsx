"use client";

import { useState } from "react";
import { Fingerprint, X } from "lucide-react";
import { ProvenanceLinkedClaim } from "@/components/report/ProvenanceLinkedClaim";
import { CodeBlock } from "@/components/shared/CodeBlock";
import type { ProvenanceLink, ProvenanceSource } from "@/types/schema";

/**
 * The static provenance demo on the landing page (screens/01-landing-page.md): a Report View mock
 * whose figures are provenance-linked numbers. Clicking a figure reveals the same structure the
 * real Provenance Panel shows (screens/10) — the exact source SQL and the backing cell — built from
 * static example data. It demonstrates the trust mechanism before signup, with no fetch on load.
 */

/** Static example claims, shaped like the report endpoint's expanded provenance links. */
const DEMO_CLAIMS: ProvenanceLink[] = [
  {
    id: "demo-claim-customers",
    report_id: "demo-report",
    claim_text: "3,420 customers",
    stated_value: "3,420",
    source_query:
      "SELECT COUNT(DISTINCT customer_id) AS customers\nFROM dataset_customers_q3_d5f3e2a1",
    source_column: "customers",
    source_row_ids: ["row-0"],
  },
  {
    id: "demo-claim-revenue",
    report_id: "demo-report",
    claim_text: "$1,204,530",
    stated_value: "$1,204,530",
    source_query:
      "SELECT SUM(trxn_amt) AS total_revenue\nFROM dataset_trxn_q3_b2c4d6e8\nWHERE customer_name = 'Acme Corp'",
    source_column: "total_revenue",
    source_row_ids: ["row-0"],
  },
];

const DEMO_NARRATIVE =
  "This quarter, 3,420 customers drove revenue of $1,204,530.";

/** The static backing source for each demo claim (mirrors GET /provenance/:claimId). */
const DEMO_SOURCES: Record<string, ProvenanceSource> = {
  "demo-claim-customers": {
    available: true,
    columns: ["customers"],
    rows: [["3420"]],
    highlight_column: "customers",
  },
  "demo-claim-revenue": {
    available: true,
    columns: ["total_revenue"],
    rows: [["1204530"]],
    highlight_column: "total_revenue",
  },
};

export function ClaimDemo() {
  const [openClaim, setOpenClaim] = useState<ProvenanceLink | null>(null);
  const source = openClaim ? (DEMO_SOURCES[openClaim.id] ?? null) : null;

  return (
    <div className="mx-auto w-full max-w-3xl">
      <div className="rounded-md border border-border bg-surface p-6 shadow-sm">
        <p className="text-caption uppercase tracking-wide text-text-secondary">
          A report from Groundwork
        </p>
        <div className="mt-3">
          <ProvenanceLinkedClaim
            narrative={DEMO_NARRATIVE}
            links={DEMO_CLAIMS}
            onClaimClick={setOpenClaim}
          />
        </div>
        <p className="mt-3 flex items-center gap-2 text-body-small text-text-secondary">
          <Fingerprint size={14} className="shrink-0 text-provenance" aria-hidden="true" />
          Every highlighted figure is clickable — each one is traced to the exact query result that
          produced it.
        </p>
      </div>

      {openClaim && source && (
        <div
          role="region"
          aria-label={`Provenance for ${openClaim.stated_value}`}
          className="mt-3 rounded-md border border-provenance/40 bg-surface-secondary p-4"
        >
          <div className="flex items-start justify-between gap-3">
            <p className="text-body-small text-text-primary">
              <span className="font-medium text-provenance">{openClaim.stated_value}</span> —{" "}
              {openClaim.claim_text}. This figure comes from the query below; the highlighted cell
              is the value the model stated.
            </p>
            <button
              type="button"
              onClick={() => setOpenClaim(null)}
              aria-label="Close provenance demo"
              className="touch-target -mr-1 -mt-1 shrink-0 rounded p-1 text-text-secondary hover:bg-surface hover:text-text-primary"
            >
              <X size={16} aria-hidden="true" />
            </button>
          </div>
          <div className="mt-3">
            <CodeBlock label="Source query" code={openClaim.source_query} />
          </div>
          {source.columns && source.rows && (
            <table className="mt-3 w-full border-collapse">
              <thead>
                <tr className="border-b border-border text-left text-caption uppercase tracking-wide text-text-secondary">
                  {source.columns.map((column) => (
                    <th key={column} className="px-2 py-1.5 font-medium">
                      {column}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {source.rows.map((row, rowIndex) => (
                  <tr key={rowIndex} className="border-b border-border">
                    {row.map((cell, cellIndex) => {
                      const column = source.columns?.[cellIndex];
                      const highlighted =
                        column === source.highlight_column &&
                        openClaim.stated_value.replace(/[$,]/g, "") ===
                          cell.replace(/,/g, "");
                      return (
                        <td
                          key={cellIndex}
                          className={
                            highlighted
                              ? "bg-provenance-light px-2 py-1.5 font-mono text-body-small text-text-primary"
                              : "px-2 py-1.5 font-mono text-body-small text-text-secondary"
                          }
                        >
                          {cell}
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  );
}
