import { AlertCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";

export function ServiceConfigurationHeader() {
  const { t } = useTranslation("setup");

  return (
    <div className="space-y-4">
      <div className="text-muted-foreground text-sm">
        {t("services.communityNote")}
      </div>
      <Alert variant="warning">
        <AlertCircle className="h-4 w-4" />
        <AlertTitle>{t("services.warning")}</AlertTitle>
        <AlertDescription>{t("services.warningText")}</AlertDescription>
      </Alert>
    </div>
  );
}
