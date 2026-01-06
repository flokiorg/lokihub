import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { useForwards } from "src/hooks/useForwards";

export function ForwardsWidget() {
  const { data: forwards } = useForwards();

  if (!forwards) {
    return null;
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Routing</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-2 gap-6">
          <div>
            <p className="text-muted-foreground text-xs">Fees Earned</p>
            <p className="text-xl font-semibold">
              <FormattedFlokicoinAmount amount={forwards.totalFeeEarnedMsat} />
              <FormattedFiatAmount
                amount={Math.floor(forwards.totalFeeEarnedMsat / 1000)}
              />
            </p>
          </div>
          <div>
            <p className="text-muted-foreground text-xs">Total Routed</p>
            <p className="text-xl font-semibold">
              <FormattedFlokicoinAmount
                amount={forwards.outboundAmountForwardedMsat}
              />
              <FormattedFiatAmount
                amount={Math.floor(forwards.outboundAmountForwardedMsat / 1000)}
              />
            </p>
          </div>
          <div>
            <p className="text-muted-foreground text-xs">Transactions Routed</p>
            <p className="text-xl font-semibold">{forwards.numForwards}</p>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
