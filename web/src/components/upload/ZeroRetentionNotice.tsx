import Link from "next/link";
import { ShieldCheck } from "lucide-react";
import { routes } from "@/lib/routes";

export interface ZeroRetentionNoticeProps {
  workspaceId: string;
}

/**
 * FEAT-044: Zero-Retention Provider Terms Notice, shown on the File Upload screen per
 * screens/05-file-upload.md's "Helper text linking to the zero-retention notice" — states plainly
 * what stays in Groundwork's infrastructure vs. what may be sent to the model, under zero-retention
 * provider terms, and links through to the fuller explanation on the Egress/Audit Log (FEAT-042).
 */
export function ZeroRetentionNotice({ workspaceId }: ZeroRetentionNoticeProps) {
  return (
    <div className="flex items-start gap-2 rounded-md border border-border bg-surface-secondary p-4 text-body-small text-text-secondary">
      <ShieldCheck size={18} className="mt-[2px] shrink-0 text-success" aria-hidden="true" />
      <p>
        Your files stay in Groundwork&apos;s infrastructure and are deleted on command. Only
        descriptions and aggregates — never raw rows — may be sent to the AI model, under
        zero-retention provider terms.{" "}
        <Link
          href={routes.auditLog(workspaceId)}
          className="text-provenance underline decoration-dotted underline-offset-2 hover:decoration-solid"
        >
          See what happens to your data
        </Link>
      </p>
    </div>
  );
}
