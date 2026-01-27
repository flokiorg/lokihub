import React from "react";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";

interface FeeDisplayProps {
  invoice: string;
  size?: "sm" | "lg";
}

export function FeeDisplay({ invoice, size = "sm" }: FeeDisplayProps) {
  const [sats, setSats] = React.useState(0);
  
  React.useEffect(() => {
     try {
         // Dynamic import to avoid breaking if not available top level
         import("@lightz/lightning-tools").then(({ Invoice }) => {
             const inv = new Invoice({ pr: invoice });
             setSats(inv.satoshi);
         });
     } catch (e) {
         console.error(e);
     }
  }, [invoice]);

  if (size === "lg") {
      return (
          <div className="text-center">
              <div className="text-2xl font-semibold">
                  <FormattedFlokicoinAmount amount={sats * 1000} />
              </div>
          </div>
      );
  }

  return (
      <div className="text-right">
          <div className="font-semibold">
              <FormattedFlokicoinAmount amount={sats * 1000} />
          </div>
      </div>
  );
}
