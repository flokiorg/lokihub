import { AlertCircle, InfoIcon } from "lucide-react";
import React from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import AppHeader from "src/components/AppHeader";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { IsolatedAppTopupDialog } from "src/components/IsolatedAppTopupDialog";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
import {
    Card,
    CardContent,
    CardDescription,
    CardFooter,
    CardHeader,
    CardTitle,
} from "src/components/ui/card";
import { LinkButton } from "src/components/ui/custom/link-button";
import { useApp } from "src/hooks/useApp";
import { copyToClipboard } from "src/lib/clipboard";
import { ConnectAppCard } from "src/screens/apps/ConnectAppCard";
import { CreateAppResponse } from "src/types";
import { appKindLabel } from "src/utils/appKind";

export function SubwalletCreated() {
  const { t } = useTranslation("apps");
  const { state } = useLocation();
  const navigate = useNavigate();
  const createAppResponse = state as CreateAppResponse | undefined;
  const { data: app } = useApp(createAppResponse?.id, true);


  const [step, setStep] = React.useState(1);

  if (!createAppResponse?.pairingUri) {
    navigate("/");
    return null;
  }

  const name = createAppResponse.name;
  let connectionSecret = createAppResponse.pairingUri;
  if (app?.metadata?.lud16) {
    connectionSecret += `&lud16=${app.metadata.lud16}`;
  }

  // Default to whichever Hub token is present (docs/nips/NIP-CASH.md /
  // NIP-CW.md "Recommended presentation") — ConnectAppCard's own toggle lets
  // the user switch to the classic NWC URI from there.
  const primaryFormat: "cashhub" | "circlehub" | "nwc" =
    createAppResponse.cashHubToken
      ? "cashhub"
      : createAppResponse.circleHubToken
        ? "circlehub"
        : "nwc";

  // A Cash Hub/Circle Hub is this screen's own thing, not a third-party app
  // you're pairing with (unlike the "nwc" case below, which covers both a
  // real connected app and a plain isolated sub-wallet — see
  // NewSimpleSubwallet.tsx, kind: "isolated" — genuinely meant to be handed
  // to some other client). Its connection is also deterministically
  // re-derivable at any time (NIP-CASH §The Pairing Connection, NIP-CW's
  // equivalent) via that hub's own "reveal" action elsewhere in the app —
  // unlike a regular app's one-time pairing secret, so the "only visible
  // now" warning below would be actively wrong for it.
  const isHub = primaryFormat === "cashhub" || primaryFormat === "circlehub";
  const kindLabel = appKindLabel(
    primaryFormat === "cashhub" ? "cash_hub" : "circle_hub"
  );

  return (
    <div className="grid gap-5">
      <AppHeader
        title={
          isHub
            ? t("subwalletCreated.hubReadyTitle", { name })
            : t("subwalletCreated.connectTitle", { name })
        }
        description=""
      />
      <div className="max-w-lg">
        <div className="flex flex-col col-span-3 gap-5 items-start">
          {step === 1 && app && (
            <div className="grid gap-5">
              <div>
                {isHub
                  ? t("subwalletCreated.hubTopUpBody", { kind: kindLabel })
                  : t("subwalletCreated.topUpBody")}
              </div>
              <div className="grid gap-5">

                {app.metadata?.lud16 && (
                  <Card>
                    <CardHeader>
                      <CardTitle>
                        {t("subwalletCreated.lightningAddressTitle")}
                      </CardTitle>
                      <CardDescription>
                        {t("subwalletCreated.lightningAddressDescription")}
                      </CardDescription>
                    </CardHeader>
                    <CardContent>
                      <p className="font-semibold">{app.metadata.lud16}</p>
                    </CardContent>
                    <CardFooter className="flex flex-row justify-end">
                      <Button
                        onClick={() => {
                          if (app.metadata?.lud16) {
                            copyToClipboard(app.metadata.lud16);
                          }
                        }}
                        size="sm"
                        variant="secondary"
                      >
                        {t("subwalletCreated.copy")}
                      </Button>
                    </CardFooter>
                  </Card>
                )}
                <Card>
                  <CardHeader>
                    <CardTitle>{name}</CardTitle>
                    <CardDescription>
                      {t("subwalletCreated.balanceLabel")}:{" "}
                      <FormattedFlokicoinAmount amount={app.balance} />
                    </CardDescription>
                  </CardHeader>
                  <CardFooter className="flex flex-row justify-end">
                    <IsolatedAppTopupDialog appId={app.id}>
                      <Button size="sm" variant="secondary">
                        {t("subwalletCreated.topUp")}
                      </Button>
                    </IsolatedAppTopupDialog>
                  </CardFooter>
                </Card>
                <Button onClick={() => setStep(2)}>
                  {t("subwalletCreated.next")}
                </Button>
              </div>
            </div>
          )}
          {step === 2 && (
            <div className="grid gap-5">
              {isHub ? (
                <>
                  <p className="text-sm text-muted-foreground">
                    {t("subwalletCreated.hubOwnConnectionIntro", {
                      kind: kindLabel,
                    })}
                  </p>
                  <Alert>
                    <InfoIcon className="h-4 w-4" />
                    <AlertDescription>
                      {t("subwalletCreated.hubConnectionReminder", {
                        kind: kindLabel,
                      })}
                    </AlertDescription>
                  </Alert>
                </>
              ) : (
                <>
                  <div className="grid gap-2">
                    <ol className="list-decimal list-inside space-y-1 text-muted-foreground">
                      <li>{t("newApp.openApp")}</li>
                      <li>{t("newApp.findSettings")}</li>
                      <li>{t("newApp.scanOrPaste")}</li>
                    </ol>
                  </div>

                  <Alert variant="destructive">
                    <AlertCircle className="h-4 w-4" />
                    <AlertTitle>Important</AlertTitle>
                    <AlertDescription className="inline">
                      For your security, these connection details are only visible now
                      and{" "}
                      <span className="font-semibold">cannot be retrieved later</span>
                      . If needed, you can store them in a password manager for future
                      reference.
                    </AlertDescription>
                  </Alert>
                </>
              )}

              {app && (
                <div className="flex justify-center">
                  <ConnectAppCard
                    app={app}
                    pairingUri={connectionSecret}
                    cashHubToken={createAppResponse.cashHubToken}
                    circleHubToken={createAppResponse.circleHubToken}
                    primaryFormat={primaryFormat}
                  />
                </div>
              )}

              <div className="flex gap-2">
                <Button onClick={() => setStep(1)} variant="secondary">
                  {t("subwalletCreated.back")}
                </Button>
                <LinkButton to="/sub-wallets">
                  {t("subwalletCreated.finish")}
                </LinkButton>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
