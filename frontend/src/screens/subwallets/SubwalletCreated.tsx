import { AlertCircle } from "lucide-react";
import React from "react";
import { useLocation, useNavigate } from "react-router-dom";
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

export function SubwalletCreated() {

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



  return (
    <div className="grid gap-5">
      <AppHeader title={`Connect ${name}`} description="" />
      <div className="max-w-lg">
        <div className="flex flex-col col-span-3 gap-5 items-start">
          {step === 1 && app && (
            <div className="grid gap-5">
              <div>
                Configure this sub-wallet by topping it up. That way it's ready to
                use as soon as it's connected.
              </div>
              <div className="grid gap-5">

                {app.metadata?.lud16 && (
                  <Card>
                    <CardHeader>
                      <CardTitle>Lightning address</CardTitle>
                      <CardDescription>
                        Your lightning address for this sub-account
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
                        Copy
                      </Button>
                    </CardFooter>
                  </Card>
                )}
                <Card>
                  <CardHeader>
                    <CardTitle>{name}</CardTitle>
                    <CardDescription>
                      Balance: <FormattedFlokicoinAmount amount={app.balance} />
                    </CardDescription>
                  </CardHeader>
                  <CardFooter className="flex flex-row justify-end">
                    <IsolatedAppTopupDialog appId={app.id}>
                      <Button size="sm" variant="secondary">
                        Top Up
                      </Button>
                    </IsolatedAppTopupDialog>
                  </CardFooter>
                </Card>
                <Button onClick={() => setStep(2)}>Next</Button>
              </div>
            </div>
          )}
          {step === 2 && (
            <div className="grid gap-5">
              <div className="grid gap-2">
                <ol className="list-decimal list-inside space-y-1 text-muted-foreground">
                  <li>Open the app you wish to connect to</li>
                  <li>
                    Find settings to connect your wallet (may be under Nostr
                    Wallet Connect or NWC)
                  </li>
                  <li>Scan or paste the connection secret</li>
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

              {app && (
                <div className="flex justify-center">
                  <ConnectAppCard app={app} pairingUri={connectionSecret} />
                </div>
              )}

              <div className="flex gap-2">
                <Button onClick={() => setStep(1)} variant="secondary">
                  Back
                </Button>
                <LinkButton to="/sub-wallets">Finish</LinkButton>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
