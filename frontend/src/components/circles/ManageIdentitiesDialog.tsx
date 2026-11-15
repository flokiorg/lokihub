import { Settings2, Trash2 } from "lucide-react";
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
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "src/components/ui/dialog";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "src/components/ui/tooltip";
import { useCircleIdentities } from "src/hooks/useCircleIdentities";
import { useDeleteCircleIdentity } from "src/hooks/useDeleteCircleIdentity";
import { CircleIdentitySummary } from "src/types";

// ManageIdentitiesDialog lets a user delete saved CircleIdentities that
// aren't attached to any circle anymore. Deletion is blocked (not just
// discouraged) while usedByCount > 0 — the backend enforces the same rule,
// this just surfaces it up front instead of via an error toast.
export function ManageIdentitiesDialog() {
  const { t } = useTranslation("circles");
  const { data } = useCircleIdentities();
  const identities = data?.identities ?? [];
  const [open, setOpen] = React.useState(false);
  const [pendingDelete, setPendingDelete] =
    React.useState<CircleIdentitySummary | null>(null);

  return (
    <>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-auto gap-1.5 px-2 py-1 text-muted-foreground"
          >
            <Settings2 className="size-3.5" />
            {t("manageIdentities.trigger")}
          </Button>
        </DialogTrigger>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("manageIdentities.title")}</DialogTitle>
            <DialogDescription>
              {t("manageIdentities.description")}
            </DialogDescription>
          </DialogHeader>
          <div className="grid max-h-80 gap-2 overflow-y-auto">
            {identities.length === 0 && (
              <p className="text-sm text-muted-foreground">
                {t("manageIdentities.noneSaved")}
              </p>
            )}
            {identities.map((identity) => {
              const inUse = identity.usedByCount > 0;
              return (
                <div
                  key={identity.id}
                  className="flex items-center justify-between gap-3 rounded-lg border p-3"
                >
                  <div className="grid min-w-0 gap-0.5">
                    <span className="truncate text-sm font-medium">
                      {identity.name}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {t(`policyLabel.${identity.policy}`)}
                    </span>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <Badge variant={inUse ? "secondary" : "outline"}>
                      {inUse
                        ? t("manageIdentities.usedBy", {
                            count: identity.usedByCount,
                          })
                        : t("manageIdentities.unused")}
                    </Badge>
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <span>
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            className="text-destructive hover:text-destructive"
                            disabled={inUse}
                            onClick={() => setPendingDelete(identity)}
                          >
                            <Trash2 className="size-4" />
                          </Button>
                        </span>
                      </TooltipTrigger>
                      {inUse && (
                        <TooltipContent>
                          {t("manageIdentities.stillUsedBy", {
                            count: identity.usedByCount,
                          })}
                        </TooltipContent>
                      )}
                    </Tooltip>
                  </div>
                </div>
              );
            })}
          </div>
        </DialogContent>
      </Dialog>

      <DeleteIdentityAlert
        identity={pendingDelete}
        onClose={() => setPendingDelete(null)}
      />
    </>
  );
}

function DeleteIdentityAlert({
  identity,
  onClose,
}: {
  identity: CircleIdentitySummary | null;
  onClose: () => void;
}) {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const { deleteIdentity, isDeleting } = useDeleteCircleIdentity();

  return (
    <AlertDialog open={!!identity} onOpenChange={(o) => !o && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("manageIdentities.deleteTitle", { name: identity?.name })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t("manageIdentities.deleteDescription")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onClose}>
            {tc("actions.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            disabled={isDeleting}
            onClick={() => identity && deleteIdentity(identity.id)}
          >
            {t("manageIdentities.delete")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
