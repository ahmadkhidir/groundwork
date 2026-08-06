import { cn } from "@/lib/cn";

export interface CodeBlockProps {
  /** Visible heading above the block, e.g. "Generated SQL". */
  label: string;
  /** The code text to show. Empty strings render an explicit empty state, never a blank box. */
  code: string;
  /** Optional caption under the block, e.g. the Phase 2 scope note on the Run Viewer. */
  hint?: string;
  className?: string;
}

/**
 * The shared SQL / Code Block component (screens.md's Shared/Common Components, screens/08-run-viewer
 * "SQL / Code Block"): a mono-spaced, scrollable code surface on the surface-secondary token.
 *
 * Deliberately read-only: inline SQL editing is a web-only Phase 3 concern (halt/amend) and belongs
 * to the Run Viewer's future editing affordance, not to this display primitive.
 */
export function CodeBlock({ label, code, hint, className }: CodeBlockProps) {
  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <div className="flex items-baseline justify-between gap-2">
        <h3 className="text-h3 text-text-primary">{label}</h3>
        {hint && <p className="text-body-small text-text-secondary">{hint}</p>}
      </div>
      {code.length > 0 ? (
        <pre
          className="overflow-x-auto rounded-md border border-border bg-surface-secondary p-3 text-body-small"
          data-testid="code-block"
        >
          <code className="block font-mono text-text-primary whitespace-pre">{code}</code>
        </pre>
      ) : (
        <p className="rounded-md border border-border bg-surface-secondary p-3 text-body-small text-text-secondary">
          No SQL generated yet.
        </p>
      )}
    </div>
  );
}
