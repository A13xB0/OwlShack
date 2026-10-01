import { cn } from "@/lib/utils";

interface PageHeaderProps {
  eyebrow?: string;
  title: string;
  meta?: React.ReactNode;
  actions?: React.ReactNode;
  className?: string;
}

// The title shrinks first, so actions stay beside it and only drop to their own line when the title itself would not fit.
export function PageHeader({
  eyebrow,
  title,
  meta,
  actions,
  className,
}: PageHeaderProps) {
  return (
    <div className={cn("space-y-0.5 border-b border-border pb-3 mb-4", className)}>
      {eyebrow && <span className="label-overline block">{eyebrow}</span>}
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <div className="flex grow basis-0 flex-wrap items-baseline gap-x-3 gap-y-1">
          <h1 className="font-mono text-lg font-semibold tracking-tight uppercase">
            {title}
          </h1>
          {meta}
        </div>
        {actions && (
          <div className="ml-auto flex flex-wrap items-center justify-end gap-2">{actions}</div>
        )}
      </div>
    </div>
  );
}

interface PageMetaProps {
  label: string;
  value: React.ReactNode;
  className?: string;
}

export function PageMeta({ label, value, className }: PageMetaProps) {
  return (
    <div className={cn("flex items-baseline gap-1.5", className)}>
      <span className="label-overline">{label}</span>
      <span className="font-mono text-sm tabular-nums">{value}</span>
    </div>
  );
}
