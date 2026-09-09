import { ArrowLeftRightIcon, CheckIcon, CopyIcon, EyeIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { AppStoreApp } from "src/components/connections/SuggestedAppData";
import Loading from "src/components/Loading";
import QRCode from "src/components/QRCode";
import { Badge } from "src/components/ui/badge";
import { useAppLogo } from "src/hooks/useAppLogo";
import { Button } from "src/components/ui/button";
import {
    Card,
    CardContent,
    CardHeader,
    CardTitle,
} from "src/components/ui/card";
import { LinkButton } from "src/components/ui/custom/link-button";
import { copyToClipboard } from "src/lib/clipboard";
import { cn } from "src/lib/utils";
import { App } from "src/types";

export function ConnectAppCard({
  app,
  pairingUri,
  lokicashToken,
  cashHubToken,
  circleHubToken,
  bearerSecret,
  appStoreApp,
  variant = "create",
  showConnectionStatus = variant === "create",
  primaryFormat = "nwc",
}: {
  app: App;
  pairingUri: string;
  // lokicashToken: the same connection as pairingUri, packaged as a single
  // lokicash1... string (NIP-CASH §The Lokicash Token).
  lokicashToken?: string;
  // cashHubToken / circleHubToken: a Cash Hub's / Circle Wallet Hub's own
  // connection, packaged as a cashhub1.../circlehub1... string (NIP-CASH
  // §The Cash Hub Connection, NIP-CW §The Circle Wallet Hub Connection) —
  // an alternative to pairingUri, toggleable by the user (see primaryFormat
  // "cashhub"/"circlehub" below), unlike lokicashToken's fixed dual-display.
  cashHubToken?: string;
  circleHubToken?: string;
  // bearerSecret: a Cash wallet's bearer redemption secret, present only
  // right after creating a bearer-mode wallet — the wallet mints it once
  // and never returns it again (NIP-CASH §Bearer Slices), so it has to be
  // shown here, not just left to a later "reveal".
  bearerSecret?: string;
  appStoreApp?: AppStoreApp;
  // "create": full Card with header, used on standalone pairing pages.
  // "reveal": bare content (no Card wrapper) for showing a secret inside a
  // Dialog that already provides its own header/footer chrome.
  variant?: "create" | "reveal";
  // Whether to show the "Waiting for app to connect..."/"App connected"
  // status block. Defaults to variant === "create", but can be set
  // independently — e.g. a Dialog-hosted "reveal" layout for a brand-new,
  // not-yet-connected wallet still wants this status shown.
  showConnectionStatus?: boolean;
  // "nwc" (default): QR + primary copy button both use pairingUri, exactly
  // as every other app connection (regular apps, circle wallets) works.
  // "lokicash": for Cash wallets specifically, where the product surface is
  // the lokicash1... token, not the raw NWC pairing URI — the QR encodes
  // lokicashToken instead, and pairingUri is never shown or copied at all.
  // REQUIRES lokicashToken to be set.
  // "cashhub"/"circlehub": for a Cash Hub's/Circle Wallet Hub's own
  // connection — REQUIRES cashHubToken/circleHubToken respectively. Unlike
  // "lokicash", this is the initial state of a user-facing toggle (see
  // "Recommended presentation" in NIP-CASH.md/NIP-CW.md): the QR/copy
  // button default to the token, with an explicit switch to reveal the
  // classic NWC URI instead, since some tooling doesn't decode this format
  // yet.
  primaryFormat?: "nwc" | "lokicash" | "cashhub" | "circlehub";
}) {
  const [timeout, setTimeout] = useState(false);
  const [isQRCodeVisible, setIsQRCodeVisible] = useState(false);
  const [showClassicNwc, setShowClassicNwc] = useState(false);
  const logoSrc = useAppLogo(appStoreApp?.id);
  const { t } = useTranslation("apps");

  const hubToken =
    primaryFormat === "cashhub"
      ? cashHubToken
      : primaryFormat === "circlehub"
        ? circleHubToken
        : undefined;
  const showingHubToken = Boolean(hubToken) && !showClassicNwc;

  const qrValue = showingHubToken
    ? (hubToken ?? "")
    : primaryFormat === "lokicash"
      ? (lokicashToken ?? "")
      : pairingUri;
  const copy = () => {
    copyToClipboard(qrValue);
  };

  useEffect(() => {
    const timeoutId = window.setTimeout(() => {
      setTimeout(true);
    }, 30000);

    return () => window.clearTimeout(timeoutId);
  }, []);

  const content = (
    <>
      {showConnectionStatus &&
        (!app.lastUsedAt ? (
          <>
            <div className="flex flex-row items-center gap-2 text-sm z-10">
              <Loading className="size-4" />
              <p>{t("newApp.waitingForConnection", "Waiting for app to connect...")}</p>
            </div>
            {timeout ? (
              <div className="text-sm flex flex-col gap-2 items-center text-center">
                {t("connectAppCard.takingLonger", "Connecting is taking longer than usual.")}
                <LinkButton to={`/apps/${app?.id}`} variant="secondary">
                  {t("connectAppCard.continueAnyway", "Continue anyway")}
                </LinkButton>
              </div>
            ) : null}
          </>
        ) : (
          <Badge variant="positive">
            <CheckIcon />
            {t("connectAppCard.appConnected", "App connected")}
          </Badge>
        ))}
      {!appStoreApp?.hideConnectionQr ? (
        <div className="relative mb-4">
          <div
            className={cn(!isQRCodeVisible ? "blur-md cursor-pointer" : "")}
            onClick={() => setIsQRCodeVisible(true)}
          >
            <QRCode value={qrValue} withIcon={!logoSrc} />
            {logoSrc ? (
              <img
                src={logoSrc}
                className="absolute w-12 h-12 top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-muted p-1 rounded-xl"
              />
            ) : null}
          </div>
          {!isQRCodeVisible ? (
            <Button
              onClick={() => {
                setIsQRCodeVisible(true);
              }}
              className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2"
            >
              <EyeIcon />
              {t("connectAppCard.revealQR", "Reveal QR")}
            </Button>
          ) : null}
        </div>
      ) : null}
      <div className="flex flex-wrap justify-center gap-2">
        {primaryFormat === "lokicash" ? (
          <Button onClick={copy} variant="outline">
            <CopyIcon />
            {t("connectAppCard.copyLokicashToken", "Copy Lokicash Token")}
          </Button>
        ) : showingHubToken ? (
          <Button onClick={copy} variant="outline">
            <CopyIcon />
            {primaryFormat === "cashhub"
              ? t("connectAppCard.copyCashHubToken", "Copy Cash Hub Token")
              : t("connectAppCard.copyCircleHubToken", "Copy Circle Hub Token")}
          </Button>
        ) : (
          <>
            <Button onClick={copy} variant="outline">
              <CopyIcon />
              {t("connectAppCard.copySecret", "Copy Connection Secret")}
            </Button>
            {/* For now not showing open in-app, only works well on Android, not on Desktop or iOS */}
            {/* <ExternalLinkButton to={pairingUri} variant="outline">
              <ExternalLinkIcon />
              Open In App
            </ExternalLinkButton> */}
            {lokicashToken ? (
              <Button
                onClick={() => copyToClipboard(lokicashToken)}
                variant="outline"
              >
                <CopyIcon />
                {t("connectAppCard.copyLokicashToken", "Copy Lokicash Token")}
              </Button>
            ) : null}
          </>
        )}
        {hubToken ? (
          // "Recommended presentation" toggle (NIP-CASH.md/NIP-CW.md §The
          // Cash/Circle Hub Connection): defaults to the token, with an
          // explicit switch to reveal the classic NWC URI for tooling that
          // doesn't decode this format yet — never a silent swap, since
          // whoever copies whichever form is on screen should know which
          // one they're sharing.
          <Button onClick={() => setShowClassicNwc((v) => !v)} variant="ghost">
            <ArrowLeftRightIcon />
            {showClassicNwc
              ? t("connectAppCard.showAsHubToken", "Show as {{prefix}}1...", {
                  prefix: primaryFormat,
                })
              : t("connectAppCard.showAsNwc", "Show as classic NWC URI")}
          </Button>
        ) : null}
        {bearerSecret ? (
          <Button
            onClick={() => copyToClipboard(bearerSecret)}
            variant="outline"
          >
            <CopyIcon />
            {t("connectAppCard.copyBearerSecret", "Copy Bearer Secret")}
          </Button>
        ) : null}
      </div>
      {bearerSecret ? (
        <p className="text-sm text-muted-foreground text-center max-w-sm">
          {t(
            "connectAppCard.bearerSecretHelper",
            "This is a bearer secret — anyone who has it can redeem this wallet's funds, with no other proof required. It's shown only this once; hand both this and the connection above to the intended recipient, out of band."
          )}
        </p>
      ) : null}
    </>
  );

  if (variant === "reveal") {
    return (
      <div className="flex flex-col items-center gap-5">{content}</div>
    );
  }

  return (
    <Card className="w-full">
      <CardHeader>
        <CardTitle className="text-center">{t("connectAppCard.connectionSecret", "Connection Secret")}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col items-center gap-5">
        {content}
      </CardContent>
    </Card>
  );
}
