import React from "react";
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
import { useDeleteApp } from "src/hooks/useDeleteApp";
import { App, ListJITWalletClaimsResponse } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

// Unlike a circle_hub (DisconnectCircleHub), a jit_hub has no partial-delete
// mode — apps.DeleteApp refuses outright if any jit_wallet child still
// exists (orphaning them would leave their parent_app_id dangling and their
// periodic reclaim job hitting an FK violation forever). So this component
// only needs a pre-flight count, not a delete-mode choice: if the hub still
// has outstanding recipients, block with an explanation instead of letting
// the user hit a raw error toast after confirming.
export function DisconnectJITHub({
  app,
  onClose,
}: {
  app: App;
  onClose: () => void;
}) {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  const [outstandingCount, setOutstandingCount] = React.useState<
    number | null
  >(null);

  React.useEffect(() => {
    (async () => {
      try {
        const data = await request<ListJITWalletClaimsResponse>(
          `/api/apps/${app.id}/jit-wallets?limit=1`
        );
        setOutstandingCount(data?.counts?.all ?? 0);
      } catch (error) {
        handleRequestError(t("disconnectJitHub.errors.check"), error);
        // Fail open to the pre-existing behavior (attempt the delete, let
        // the backend guard reject it) rather than blocking deletion of an
        // otherwise-empty hub just because this check failed.
        setOutstandingCount(0);
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [app.id]);

  const { deleteApp, isDeleting } = useDeleteApp(app, () => {
    navigate(
      app.metadata?.app_store_app_id !== SUBWALLET_APPSTORE_APP_ID
        ? "/apps?tab=connected-apps"
        : "/sub-wallets"
    );
  });

  const isLoading = outstandingCount === null;
  const hasOutstanding = (outstandingCount ?? 0) > 0;

  return (
    <AlertDialog open>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("disconnectJitHub.title")}</AlertDialogTitle>
          <AlertDialogDescription asChild>
            {isLoading ? (
              <p>{t("disconnectJitHub.checking")}</p>
            ) : hasOutstanding ? (
              <p>
                {t("disconnectJitHub.hasOutstanding", {
                  count: outstandingCount,
                })}
              </p>
            ) : (
              <>{t("disconnectJitHub.safeToDelete")}</>
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onClose} disabled={isDeleting}>
            {hasOutstanding
              ? t("disconnectJitHub.close")
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
