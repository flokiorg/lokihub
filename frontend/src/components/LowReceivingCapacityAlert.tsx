import { AlertTriangleIcon } from "lucide-react";
import { Link } from "react-router";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "src/components/ui/alert.tsx";

export default function LowReceivingCapacityAlert({
  jitAvailable = false,
}: {
  jitAvailable?: boolean;
}) {
  return (
    <Alert variant="warning">
      <AlertTriangleIcon className="h-4 w-4" />
      <AlertTitle>Low receiving capacity</AlertTitle>
      <AlertDescription className="inline">
        You likely won't be able to receive payments until you{" "}
        <Link className="underline" to="/wallet/send">
          spend
        </Link>
        {jitAvailable && " or purchase liquidity via JIT"}.
      </AlertDescription>
    </Alert>
  );
}
