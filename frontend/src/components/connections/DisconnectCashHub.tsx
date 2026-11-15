import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";

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
import { useCashHubDeleteCheck } from "src/hooks/useCashHub";
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
  const outstandingCount = liveBillsError ? 0 : (liveBills?.totalCount ?? null);

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

  // Beyond live bills, a hub stays while a request record naming it is
  // retained: the delete check lists each blocker and the earliest time.
  const { data: deleteCheck } = useCashHubDeleteCheck(app.id);
  const recordBlockers = (deleteCheck?.blockers ?? []).filter(
    (b) => b.kind !== "live_bills"
  );

  const isLoading = outstandingCount === null;
  // Deleting ends the watch on paid on-chain order addresses: the owner confirms that.
  const confirmWatchEnd = !!deleteCheck?.confirmation_required;
  const hasOutstanding =
    (outstandingCount ?? 0) > 0 || recordBlockers.length > 0;

  return (
    <AlertDialog open>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("disconnectCashHub.title")}</AlertDialogTitle>
          <AlertDialogDescription asChild>
            {isLoading ? (
              <p>{t("disconnectCashHub.checking")}</p>
            ) : hasOutstanding ? (
              <div className="space-y-2">
                {(outstandingCount ?? 0) > 0 && (
                  <p>
                    {t("disconnectCashHub.hasOutstanding", {
                      count: outstandingCount,
                    })}
                  </p>
                )}
                {recordBlockers.map((b) => (
                  <p key={b.kind}>
                    {b.kind === "payout_open"
                      ? t("disconnectCashHub.payoutsOpen", { count: b.count })
                      : b.until
                        ? t("disconnectCashHub.recordsUntil", {
                            count: b.count,
                            until: new Date(b.until * 1000).toLocaleString(),
                          })
                        : t("disconnectCashHub.recordsUntilBillsGone", {
                            count: b.count,
                          })}
                  </p>
                ))}
                {deleteCheck && recordBlockers.length > 0 && (
                  <p className="text-sm text-muted-foreground">
                    {deleteCheck.earliest_delete_at
                      ? t("disconnectCashHub.earliestDelete", {
                          at: new Date(
                            deleteCheck.earliest_delete_at * 1000
                          ).toLocaleString(),
                        })
                      : t("disconnectCashHub.earliestAfterBills")}
                  </p>
                )}
              </div>
            ) : (
              <div className="space-y-2">
                <p>{t("disconnectCashHub.safeToDelete")}</p>
                {confirmWatchEnd && (
                  <p>
                    {t("disconnectCashHub.watchEnds", {
                      count: deleteCheck?.watched_addresses,
                    })}
                  </p>
                )}
                {!!deleteCheck?.listed_offers &&
                  deleteCheck.listings_delete_due_at && (
                    <p className="text-sm text-muted-foreground">
                      {t("disconnectCashHub.listedOffers", {
                        count: deleteCheck.listed_offers,
                        at: new Date(
                          deleteCheck.listings_delete_due_at * 1000
                        ).toLocaleString(),
                      })}
                    </p>
                  )}
              </div>
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
            <AlertDialogAction
              onClick={() =>
                deleteApp(confirmWatchEnd ? { confirmWatchEnd } : undefined)
              }
              disabled={isDeleting || !deleteCheck}
            >
              {confirmWatchEnd
                ? t("disconnectCashHub.confirmWatchEnd")
                : tc("actions.confirm")}
            </AlertDialogAction>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
