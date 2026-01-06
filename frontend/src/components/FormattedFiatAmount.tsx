import { Skeleton } from "src/components/ui/skeleton";
import { useFlokicoinRate } from "src/hooks/useFlokicoinRate";
import { useInfo } from "src/hooks/useInfo";
import { cn } from "src/lib/utils";

type FormattedFiatAmountProps = {
  amount: number;
  className?: string;
  showApprox?: boolean;
};

export default function FormattedFiatAmount({
  amount,
  className,
  showApprox,
}: FormattedFiatAmountProps) {
  const { data: info } = useInfo();
  const { data: flokicoinRate, error: flokicoinRateError } = useFlokicoinRate();

  if (info?.currency === "LOKI" || flokicoinRateError) {
    return null;
  }

  return (
    <div className={cn("text-sm text-muted-foreground", className)}>
      {showApprox && flokicoinRate && "~"}
      {!flokicoinRate ? (
        <Skeleton className="w-20">&nbsp;</Skeleton>
      ) : (
        new Intl.NumberFormat("en-US", {
          style: "currency",
          currency: info?.currency || "usd",
        }).format((amount / 100_000_000) * flokicoinRate.rate_float)
      )}
    </div>
  );
}
