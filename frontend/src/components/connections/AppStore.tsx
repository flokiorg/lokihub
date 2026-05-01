import { CirclePlusIcon } from "lucide-react";
import ResponsiveExternalLinkButton from "src/components/ResponsiveExternalLinkButton";
import SuggestedApps from "src/components/connections/SuggestedApps";
import { useTranslation } from "react-i18next";

function AppStore() {
  const { t } = useTranslation("apps");
  return (
    <>
      <div className="flex flex-col flex-1">
        <div className="flex justify-between items-center">
          <div className="flex-1">
            <h1 className="text-xl lg:text-2xl font-semibold">{t("connections.appStore", "App Store")}</h1>
          </div>
          <div className="flex gap-3 h-full">
            <ResponsiveExternalLinkButton
              icon={CirclePlusIcon}
              text={t("appStore.submitApp", "Submit your app")}
              variant="outline"
              to="https://github.com/flokiorg/lokihub-store"
            />
          </div>
        </div>
      </div>
      <SuggestedApps />
    </>
  );
}

export default AppStore;
