import { Skeleton } from "src/components/ui/skeleton";
import { useFiatRate } from "src/hooks/useFiatRate";
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
  const { data: fiatRate, error: fiatRateError } = useFiatRate();

  if (info?.currency === "LOKI" || fiatRateError) {
    return null;
  }

  return (
    <div className={cn("text-sm text-muted-foreground", className)}>
      {showApprox && fiatRate && "~"}
      {!fiatRate ? (
        <Skeleton className="w-20">&nbsp;</Skeleton>
      ) : (
        new Intl.NumberFormat("en-US", {
          style: "currency",
          currency: info?.currency || "usd",
        }).format((amount / 100_000_000) * fiatRate.rate_float)
      )}
    </div>
  );
}
