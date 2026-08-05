export interface PayloadViewerProps {
  payload: string;
}

/**
 * screens.md's shared SQL / Code Block pattern, applied here to `payload_sent_to_model`: monospace
 * (`code` type token), `surface-secondary` background since this is always read-only here, JSON/text
 * rendered verbatim rather than as prose — per screens/17-egress-audit-log.md, "the exact
 * `payload_sent_to_model`... since it is JSON/text, not prose."
 */
export function PayloadViewer({ payload }: PayloadViewerProps) {
  if (!payload) {
    return (
      <p className="text-body-small text-text-secondary">
        No model payload for this action.
      </p>
    );
  }

  let formatted = payload;
  try {
    formatted = JSON.stringify(JSON.parse(payload), null, 2);
  } catch {
    // Not JSON — render the raw string verbatim rather than fail.
  }

  return (
    <pre className="max-h-80 overflow-auto rounded-md border border-border bg-surface-secondary p-4 text-code text-code-accent">
      <code>{formatted}</code>
    </pre>
  );
}
