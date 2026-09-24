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
import { SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { useApps } from "src/hooks/useApps";
import { useDeleteApp } from "src/hooks/useDeleteApp";
import { App } from "src/types";

// Unlike a circle_hub (DisconnectCircleHub), a cash_hub has no partial-delete
// mode — apps.DeleteApp refuses outright if any cash_wallet child still
// exists (orphaning them would leave their parent_app_id dangling and their
// periodic reclaim job hitting an FK violation forever). So this component
// only needs a pre-flight count, not a delete-mode choice: if the hub still
// has outstanding recipients, block with an explanation instead of letting
// the user hit a raw error toast after confirming.
export function DisconnectCashHub({
  app,
  onClose,
}: {
  app: App;
  onClose: () => void;
}) {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  // Count the hub's LIVE bills, which is the backend's own predicate
  // (apps.deleteHubAppTx counts child app rows with this parent).
  //
  // Deliberately NOT the claim counts this used to read: those are a union of
  // live and archived slices, so a hub whose every bill had been reclaimed or
  // deleted still reported its whole history and could never be deleted — the
  // guard blocked on records of bills rather than on bills. An archived bill
  // has no app row at all, so it can orphan nothing and the backend never
  // refuses over one.
  const { data: liveBills, error: liveBillsError } = useApps(1, 1, {
    parentAppId: app.id,
  });
  // Fail open on error, matching the previous behaviour: let the backend
  // guard reject the delete rather than blocking an otherwise-empty hub
  // because this count failed.
  const outstandingCount = liveBillsError
    ? 0
    : (liveBills?.totalCount ?? null);

  const { deleteApp, isDeleting } = useDeleteApp(
    app,
    () => {
      navigate(
        app.metadata?.app_store_app_id !== SUBWALLET_APPSTORE_APP_ID
          ? "/apps?tab=connected-apps"
          : "/cash-hub"
      );
    },
    { success: t("disconnectCashHub.deletedToast") }
  );

  const isLoading = outstandingCount === null;
  const hasOutstanding = (outstandingCount ?? 0) > 0;

  return (
    <AlertDialog open>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("disconnectCashHub.title")}</AlertDialogTitle>
          <AlertDialogDescription asChild>
            {isLoading ? (
              <p>{t("disconnectCashHub.checking")}</p>
            ) : hasOutstanding ? (
              <p>
                {t("disconnectCashHub.hasOutstanding", {
                  count: outstandingCount,
                })}
              </p>
            ) : (
              <>{t("disconnectCashHub.safeToDelete")}</>
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onClose} disabled={isDeleting}>
            {hasOutstanding
              ? t("disconnectCashHub.close")
              : tc("actions.cancel")}
          </AlertDialogCancel>
          {!isLoading && !hasOutstanding && (
            <AlertDialogAction onClick={deleteApp} disabled={isDeleting}>
              {tc("actions.confirm")}
            </AlertDialogAction>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
