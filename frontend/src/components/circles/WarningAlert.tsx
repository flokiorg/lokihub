import { AlertTriangleIcon } from "lucide-react";
import React from "react";

import { Alert, AlertDescription } from "src/components/ui/alert";

export function WarningAlert({ children }: { children: React.ReactNode }) {
  return (
    <Alert variant="warning">
      <AlertTriangleIcon className="h-4 w-4" />
      <AlertDescription>{children}</AlertDescription>
    </Alert>
  );
}
