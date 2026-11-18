import React from "react";
import { useTranslation } from "react-i18next";
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
import { regenerateCashHubConnection } from "src/hooks/useListings";
import { handleRequestError } from "src/utils/handleRequestError";

// RegenerateCashHubConnection revokes a Cash Hub's connection at once and shows
// the new one once (WP-25 item 7): for when its string was ever published. A
// Cash Hub is never listed.
//
// Controlled, trigger-less: revoke is a rare, semi-destructive action, so it
// lives in the detail page's "…" menu rather than as a standing full-width
// button. The host opens the dialog from there; this renders only the
// confirm dialog and the shown-once new connection.
export function RegenerateCashHubConnection({
  hubId,
  open,
  onOpenChange,
}: {
  hubId: number;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation("listings");
  const [token, setToken] = React.useState<string | undefined>();
  const regenerate = async () => {
    try {
      const result = await regenerateCashHubConnection(hubId);
      setToken(result?.cashHubToken);
    } catch (error) {
      handleRequestError(t("errors.regenerate"), error);
    }
  };
  return (
    <>
      <AlertDialog open={open} onOpenChange={onOpenChange}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("regenerate.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("regenerate.description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("regenerate.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={regenerate}>
              {t("regenerate.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {token && (
        <div className="space-y-1 text-xs">
          <p className="font-medium">{t("regenerate.shownOnce")}</p>
          <p className="break-all font-mono" data-testid="new-cash-hub-token">
            {token}
          </p>
        </div>
      )}
    </>
  );
}
