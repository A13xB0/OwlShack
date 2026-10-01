import type { ComponentProps } from "react";
import { Link } from "react-router-dom";
import { Loader2, type LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

type Tone = "primary" | "secondary" | "destructive";

const TONE: Record<Tone, { variant: "default" | "outline" | "destructive"; className?: string }> = {
  primary: { variant: "default" },
  secondary: {
    variant: "outline",
    className: "border-border bg-transparent text-muted-foreground shadow-none hover:bg-transparent hover:text-primary dark:bg-transparent dark:hover:bg-transparent",
  },
  destructive: { variant: "destructive" },
};

type HeaderButtonProps = Omit<ComponentProps<"button">, "children"> & {
  icon: LucideIcon;
  // One primary per header, rightmost; secondary for the rest; destructive only to confirm.
  tone?: Tone;
  // A spinner replaces the icon while the action runs.
  busy?: boolean;
  // A toggle that is on.
  active?: boolean;
  // Navigates instead of acting.
  to?: string;
  // Square and unlabelled; children become the aria-label.
  iconOnly?: boolean;
  children: React.ReactNode;
};

export function HeaderButton({
  icon: Icon,
  tone = "secondary",
  busy = false,
  active,
  to,
  iconOnly = false,
  className,
  children,
  ...props
}: HeaderButtonProps) {
  const t = TONE[tone];
  const glyph = busy ? <Loader2 className="animate-spin" /> : <Icon />;
  const cls = cn(t.className, active && "border-primary text-primary", className);
  const size = iconOnly ? "icon-header" : "header";
  const label = typeof children === "string" && iconOnly ? children : undefined;
  if (to) {
    return (
      <Button asChild variant={t.variant} size={size} className={cls}>
        <Link to={to} aria-label={label}>
          {glyph}
          {iconOnly ? null : children}
        </Link>
      </Button>
    );
  }
  return (
    <Button variant={t.variant} size={size} className={cls} aria-label={label} aria-pressed={active} {...props}>
      {glyph}
      {iconOnly ? null : children}
    </Button>
  );
}
