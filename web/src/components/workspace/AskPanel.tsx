import { Send } from "lucide-react";
import { Input } from "@/components/ui/Input";
import { Button } from "@/components/ui/Button";

/**
 * The Ask panel's visual shell — screens/04-workspace-home.md's "large TextField... with a Submit
 * IconButton" (mobile) / "full-width TextInput + Submit Button" (web).
 *
 * FEAT-013 (Natural-Language Question → SQL Generation, Phase 2) owns making this control actually
 * answer a question, and FEAT-012 (Wake-on-Intent, Phase 5) owns triggering an engine wake when it is
 * focused. FEAT-011 (this wave) is scoped to the engine lifecycle and its honest status display, not
 * the query loop — so the control is laid out per spec but genuinely disabled and visibly captioned,
 * rather than silently accepting input that goes nowhere (which would be a dishonest status of its
 * own, exactly what branding.md's honest-status principle rules out).
 */
export function AskPanel() {
  return (
    <div className="flex flex-col gap-2">
      <form
        className="flex items-end gap-2"
        onSubmit={(event) => event.preventDefault()}
        aria-describedby="ask-panel-caption"
      >
        <div className="flex-1">
          <Input
            label="Ask a question"
            placeholder="Ask a question about your data..."
            disabled
          />
        </div>
        <Button type="submit" variant="primary" disabled className="shrink-0">
          <Send size={16} aria-hidden="true" />
          <span className="sr-only">Submit question</span>
        </Button>
      </form>
      <p id="ask-panel-caption" className="text-caption text-text-secondary">
        Asking questions arrives in Phase 2 (FEAT-013).
      </p>
    </div>
  );
}
