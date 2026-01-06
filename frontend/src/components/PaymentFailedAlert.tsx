import { TriangleAlertIcon } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";

export function PaymentFailedAlert({
  errorMessage,
}: {
  invoice: string;
  errorMessage: string;
}) {

  return (
    <Alert>
      <TriangleAlertIcon className="h-4 w-4" />
      <AlertTitle>Payment Failed</AlertTitle>
      <AlertDescription>
        <p>{errorMessage}</p>
        <p>
          Try the payment again, review our FAQ, or contact the community on Discord for help with failed payments.
        </p>
      </AlertDescription>
    </Alert>
  );
}
