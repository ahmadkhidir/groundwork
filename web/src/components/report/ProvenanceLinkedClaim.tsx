"use client";

import type { ReactNode } from "react";
import type { ProvenanceLink } from "@/types/schema";

export interface ProvenanceLinkedClaimProps {
  narrative: string;
  /** The report's claim-level provenance (FEAT-030), expanded from ids by the report endpoint. */
  links: ProvenanceLink[];
  /** Opens the provenance panel (screens/10) for the clicked figure. */
  onClaimClick: (link: ProvenanceLink) => void;
}

/**
 * The Provenance-Linked Number (screens/09-report-view.md, FEAT-030): renders the report narrative
 * with every traceable figure wrapped as a clickable, verified claim — a distinct visual signal
 * (the `provenance` color), never styled as an ordinary link.
 *
 * Matching is by exact claim text: each link's `claim_text` is the narrative span it corresponds to,
 * so the component walks the narrative and wraps the first occurrence of each span. A span that no
 * longer appears in the text (narrative changed after materialization) is skipped — the narrative
 * still reads correctly, the figure just is not clickable, matching screens/09's non-interactive
 * edge case without erroring the report.
 */
export function ProvenanceLinkedClaim({
  narrative,
  links,
  onClaimClick,
}: ProvenanceLinkedClaimProps) {
  const nodes: ReactNode[] = [];
  let cursor = 0;

  for (const link of links) {
    const span = link.claim_text.trim();
    if (!span) continue;

    const index = narrative.indexOf(span, cursor);
    if (index < 0) {
      // The span is not in the narrative (or appears before the cursor) — leave it as plain text.
      continue;
    }
    if (index > cursor) {
      nodes.push(narrative.slice(cursor, index));
    }
    nodes.push(
      <button
        key={link.id}
        type="button"
        onClick={() => onClaimClick(link)}
        aria-label={`${link.stated_value} — ${link.claim_text}. Show provenance.`}
        className="text-provenance underline decoration-dotted underline-offset-2 hover:text-provenance focus:outline-2 focus:outline-provenance"
      >
        {narrative.slice(index, index + span.length)}
      </button>,
    );
    cursor = index + span.length;
  }

  if (cursor < narrative.length) {
    nodes.push(narrative.slice(cursor));
  }

  return <p className="text-body-large whitespace-pre-line text-text-primary">{nodes}</p>;
}