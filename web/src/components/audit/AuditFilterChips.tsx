import { cn } from "@/lib/cn";
import { AUDIT_ACTION_LABELS, AUDIT_ACTION_TYPES, type AuditActionTypeFilter } from "./types";

export interface AuditFilterChipsProps {
  value: AuditActionTypeFilter | "";
  onChange: (value: AuditActionTypeFilter | "") => void;
}

/**
 * screens/17-egress-audit-log.md's action-type filter chips: Query, Model Call, Export, Deletion,
 * Login — horizontally scrollable on mobile, same row on web above the table.
 */
export function AuditFilterChips({ value, onChange }: AuditFilterChipsProps) {
  return (
    <div className="flex gap-2 overflow-x-auto pb-1" role="group" aria-label="Filter by action type">
      <Chip label="All" active={value === ""} onClick={() => onChange("")} />
      {AUDIT_ACTION_TYPES.map((type) => (
        <Chip
          key={type}
          label={AUDIT_ACTION_LABELS[type]}
          active={value === type}
          onClick={() => onChange(type)}
        />
      ))}
    </div>
  );
}

function Chip({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "shrink-0 rounded-full border px-4 py-1 text-body-small font-medium whitespace-nowrap",
        active
          ? "border-primary bg-primary-light text-primary"
          : "border-border bg-surface text-text-secondary hover:bg-surface-secondary",
      )}
    >
      {label}
    </button>
  );
}
