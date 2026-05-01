import { GlobeIcon } from "lucide-react";
import { AppStoreApp } from "src/components/connections/SuggestedAppData";
import { AppleIcon } from "src/components/icons/Apple";
import { ChromeIcon } from "src/components/icons/Chrome";
import { FirefoxIcon } from "src/components/icons/Firefox";
import { PlayStoreIcon } from "src/components/icons/PlayStore";
import { ZapStoreIcon } from "src/components/icons/ZapStore";
import {
  Card,
  CardFooter,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { ExternalLinkButton } from "src/components/ui/custom/external-link-button";
import { useTranslation } from "react-i18next";

export function AppLinksCard({ appStoreApp }: { appStoreApp: AppStoreApp }) {
  const { t } = useTranslation("apps");

  if (
    !appStoreApp.appleLink &&
    !appStoreApp.playLink &&
    !appStoreApp.zapStoreLink &&
    !appStoreApp.chromeLink &&
    !appStoreApp.firefoxLink &&
    !appStoreApp.webLink
  ) {
    return null;
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-2xl">{t("appStore.links", "Links")}</CardTitle>
      </CardHeader>
      <CardFooter className="flex flex-row flex-wrap gap-2">
        {appStoreApp.webLink && (
          <ExternalLinkButton to={appStoreApp.webLink} variant="outline">
            <GlobeIcon />
            Website
          </ExternalLinkButton>
        )}
        {appStoreApp.playLink && (
          <ExternalLinkButton to={appStoreApp.playLink} variant="outline">
            <PlayStoreIcon />
            Play Store
          </ExternalLinkButton>
        )}
        {appStoreApp.appleLink && (
          <ExternalLinkButton to={appStoreApp.appleLink} variant="outline">
            <AppleIcon />
            App Store
          </ExternalLinkButton>
        )}
        {appStoreApp.zapStoreLink && (
          <ExternalLinkButton to={appStoreApp.zapStoreLink} variant="outline">
            <ZapStoreIcon />
            Zapstore
          </ExternalLinkButton>
        )}
        {appStoreApp.chromeLink && (
          <ExternalLinkButton to={appStoreApp.chromeLink} variant="outline">
            <ChromeIcon />
            Chrome Web Store
          </ExternalLinkButton>
        )}
        {appStoreApp.firefoxLink && (
          <ExternalLinkButton to={appStoreApp.firefoxLink} variant="outline">
            <FirefoxIcon />
            Firefox Add-Ons
          </ExternalLinkButton>
        )}
      </CardFooter>
    </Card>
  );
}
