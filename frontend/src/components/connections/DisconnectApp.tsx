import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "src/components/ui/alert-dialog";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { useDeleteApp } from "src/hooks/useDeleteApp";
import { App } from "src/types";
import { appKindLabel } from "src/utils/appKind";

// The fallback delete dialog for everything AppDetails.tsx doesn't give a
// dedicated component to: a real third-party app, an isolated sub-wallet,
// or — unlike cash_hub/circle_hub, which get DisconnectCashHub/
// DisconnectCircleHub — a cash_wallet/circle_wallet child. Those two kinds
// need their own branch here rather than the generic "connected apps will
// lose access" copy below, which describes revoking a third party, not
// deleting a wallet you issued. They also need an honest warning:
// apps.DeleteApp's default case (db/apps_service.go) is a plain row
// delete for these kinds — unlike a hub, there is no pre-flight
// outstanding-balance check, so a nonzero balance really would be lost.
export function DisconnectApp({
  app,
  onClose,
}: {
  app: App;
  onClose: () => void;
}) {
  const { t } = useTranslation("apps");
  const navigate = useNavigate();

  const isWallet = app.kind === "cash_wallet" || app.kind === "circle_wallet";
  const kind = appKindLabel(app.kind);

  const { deleteApp, isDeleting } = useDeleteApp(
    app,
    () => {
      navigate(
        app.metadata?.app_store_app_id !== SUBWALLET_APPSTORE_APP_ID
          ? "/apps?tab=connected-apps"
          : "/sub-wallets"
      );
    },
    isWallet
      ? { success: t("disconnectApp.walletDeletedToast", { kind }) }
      : undefined
  );

  // Check if this is a sub-wallet with a lightning address
  const isSubwallet =
    app.metadata?.app_store_app_id === SUBWALLET_APPSTORE_APP_ID;
  const hasLightningAddress = !!app.metadata?.lud16;
  const hasBalance = app.balance > 0;

  return (
    <AlertDialog open>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {isWallet
              ? t("disconnectApp.walletTitle", { kind })
              : t("disconnectApp.appTitle")}
          </AlertDialogTitle>
          <AlertDialogDescription asChild>
            <div className="space-y-2">
              {isWallet ? (
                hasBalance ? (
                  <>
                    <p>
                      {t("disconnectApp.walletHasBalanceWarning", { kind })}
                    </p>
                    <p className="font-medium">
                      <FormattedFlokicoinAmount amount={app.balance} />
                    </p>
                    <p>{t("disconnectApp.walletHasBalanceNote")}</p>
                  </>
                ) : (
                  <p>{t("disconnectApp.walletEmptyNote", { kind })}</p>
                )
              ) : (
                <p>
                  {t("disconnectApp.connectedAppsWarning")}
                  {app.isolated && (
                    <> {t("disconnectApp.isolatedFundsSafe")}</>
                  )}
                </p>
              )}
              {isSubwallet && hasLightningAddress && (
                <p className="font-medium mt-4">
                  {t("disconnectApp.lightningAddressWarning", {
                    lud16: app.metadata?.lud16,
                  })}
                </p>
              )}
            </div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onClose}>
            {t("disconnectApp.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction onClick={deleteApp} disabled={isDeleting}>
            {t("disconnectApp.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
