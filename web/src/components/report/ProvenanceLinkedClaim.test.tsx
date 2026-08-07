import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ProvenanceLinkedClaim } from "./ProvenanceLinkedClaim";
import type { ProvenanceLink } from "@/types/schema";

const LINKS: ProvenanceLink[] = [
  {
    id: "claim-1",
    report_id: "report-1",
    claim_text: "10 customers in North",
    stated_value: "10",
    source_query: "SELECT region, count(*) FROM customers",
    source_column: "cnt",
    source_row_ids: ["0"],
  },
];

describe("ProvenanceLinkedClaim", () => {
  it("wraps each traceable figure as a clickable provenance button", () => {
    const onClick = vi.fn();
    render(
      <ProvenanceLinkedClaim
        narrative="North leads with 10 customers in North overall."
        links={LINKS}
        onClaimClick={onClick}
      />,
    );

    const claim = screen.getByRole("button", { name: /10 — 10 customers in North/ });
    expect(claim).toBeInTheDocument();
    // The first occurrence is wrapped; surrounding narrative text stays plain.
    expect(screen.getByText(/^North leads with/)).toBeInTheDocument();
  });

  it("invokes the click handler with the claim", () => {
    const onClick = vi.fn();
    render(
      <ProvenanceLinkedClaim
        narrative="10 customers in North."
        links={LINKS}
        onClaimClick={onClick}
      />,
    );
    screen.getByRole("button", { name: /Show provenance/ }).click();
    expect(onClick).toHaveBeenCalledWith(LINKS[0]);
  });

  it("leaves a claim whose span is absent from the narrative as plain text", () => {
    render(
      <ProvenanceLinkedClaim
        narrative="The report changed and the figure is gone."
        links={LINKS}
        onClaimClick={vi.fn()}
      />,
    );
    // No figure is clickable; the narrative still renders as readable text.
    expect(screen.queryByRole("button", { name: /Show provenance/ })).not.toBeInTheDocument();
    expect(screen.getByText(/The report changed/)).toBeInTheDocument();
  });
});