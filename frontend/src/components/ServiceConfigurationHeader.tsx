import { AlertCircle } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import ExternalLink from "src/components/ExternalLink";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";

export function ServiceConfigurationHeader() {
  const { t } = useTranslation("setup");

  return (
    <div className="space-y-4">
      <div className="text-muted-foreground text-sm">
        <Trans
          i18nKey="services.communityNote"
          ns="setup"
          components={[
            <span />,
            <ExternalLink
              to="https://github.com/flokiorg/lokihub-services"
              className="underline underline-offset-4"
            >
              lokihub-services
            </ExternalLink>,
          ]}
        />
      </div>
      <Alert variant="warning">
        <AlertCircle className="h-4 w-4" />
        <AlertTitle>{t("services.warning")}</AlertTitle>
        <AlertDescription>{t("services.warningText")}</AlertDescription>
      </Alert>
    </div>
  );
}
