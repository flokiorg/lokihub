import { RotateCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardFooter,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { AppError } from "src/utils/request";

type GlobalErrorProps = {
  error: Error;
};

export function GlobalError({ error }: GlobalErrorProps) {
  const { t } = useTranslation("common");

  const isAppError = error instanceof AppError;
  const status = isAppError ? (error as AppError).status : undefined;
  const url = isAppError ? (error as AppError).url : undefined;
  const displayMessage = error.message;

  const helpText = !status
    ? "Cannot reach the server. Check your network connection or if the backend is running."
    : status === 401 || status === 403
      ? "Authentication failed. Your session may have expired — try reloading."
      : status >= 500
        ? "The server encountered an internal error. Check the backend logs."
        : undefined;

  return (
    <div className="flex items-center justify-center min-h-screen p-4 bg-background overflow-y-auto">
      <Card className="w-full max-w-md my-8">
        <CardHeader>
          <CardTitle className="text-destructive">
            {t("criticalError.title")}
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-muted-foreground">
            {t("criticalError.description")}
          </p>
          <div className="space-y-3">
            {status && <Badge variant="destructive">HTTP {status}</Badge>}
            <div className="p-3 bg-muted rounded-md font-mono text-sm break-all max-h-40 overflow-y-auto">
              {displayMessage}
            </div>
            {url && (
              <p className="text-xs text-muted-foreground font-mono break-all">
                Endpoint: {url}
              </p>
            )}
            {helpText && (
              <p className="text-sm text-muted-foreground">{helpText}</p>
            )}
          </div>
        </CardContent>
        <CardFooter className="flex justify-end gap-2">
          <Button onClick={() => window.location.reload()}>
            <RotateCw className="w-4 h-4 me-2" />
            {t("criticalError.reloadApp")}
          </Button>
        </CardFooter>
      </Card>
    </div>
  );
}
