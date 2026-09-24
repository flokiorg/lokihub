import { useTranslation } from "react-i18next";
import { TooltipRow } from "src/components/charts/chartTheme";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";

// A tooltip that explains a segment rather than naming it.
//
// recharts' default prints "name: number" and nothing else, which is why
// hovering the old charts told you almost nothing: no units, no sense of
// whether a bar was big, and no way to see what a stacked segment was a part
// of. This carries the formatted amount, how many bills it is, and the
// segment's share of the point it belongs to — the three things that turn a
// bar into a fact.
export function RichTooltip({
  active,
  label,
  rows,
  total,
  footer,
}: {
  active?: boolean;
  label?: string;
  rows: TooltipRow[];
  total?: number;
  footer?: string;
}) {
  const { t } = useTranslation("circles");
  if (!active || !rows.length) {
    return null;
  }
  return (
    <div className="bg-popover text-popover-foreground rounded-(--radius) border p-2.5 text-xs shadow-md">
      {label && <p className="mb-1.5 font-medium">{label}</p>}
      <div className="grid gap-1">
        {rows.map((row) => {
          const share =
            total && total > 0 ? Math.round((row.mloki / total) * 100) : null;
          return (
            <div key={row.name} className="flex items-center gap-2">
              <span
                aria-hidden
                className="size-2 shrink-0 rounded-[2px]"
                style={{ background: row.color }}
              />
              <span className="text-muted-foreground">{row.name}</span>
              <span className="ml-auto font-medium tabular-nums">
                <FormattedFlokicoinAmount amount={row.mloki} />
              </span>
              {share !== null && (
                <span className="text-muted-foreground w-8 text-right tabular-nums">
                  {share}%
                </span>
              )}
            </div>
          );
        })}
      </div>
      {rows.some((r) => r.count !== undefined) && (
        <p className="text-muted-foreground mt-1.5">
          {t("cashCharts.billsTooltip", {
            count: rows.reduce((n, r) => n + (r.count ?? 0), 0),
          })}
        </p>
      )}
      {footer && <p className="text-muted-foreground mt-1.5">{footer}</p>}
    </div>
  );
}
