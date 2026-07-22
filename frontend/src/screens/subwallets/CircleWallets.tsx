import { Trash2Icon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { useSWRConfig } from "swr";
import { NostrProfileRow } from "src/components/circles/NostrProfileRow";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "src/components/ui/alert-dialog";
import { Button } from "src/components/ui/button";
import { Checkbox } from "src/components/ui/checkbox";
import { CustomPagination } from "src/components/CustomPagination";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { LIST_CIRCLE_CHILDREN_LIMIT } from "src/constants";
import { useCircleChildren } from "src/hooks/useCircleChildren";
import { useNostrProfiles } from "src/hooks/useNostrProfiles";
import { cn } from "src/lib/utils";
import { CircleChildBalance } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

type CircleWalletsProps = {
  appId: number;
};

// Lists the circle_wallet children already instantiated for this circle_hub
// (distinct from CircleAllowlist, which manages pubkeys eligible to request
// one but not-yet-instantiated). Removing a child reclaims any remaining
// balance back to the hub and deletes the wallet — unlike DeleteCircleHub,
// which only ever operates on the whole hub at once. Balances are polled so
// that member spending shows up here without a manual refresh, matching the
// transaction list's auto-load behavior.
export function CircleWallets({ appId }: CircleWalletsProps) {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  const { mutate } = useSWRConfig();
  const [page, setPage] = React.useState(1);
  const [confirmDeleteChild, setConfirmDeleteChild] =
    React.useState<CircleChildBalance | null>(null);
  const [isDeleting, setDeleting] = React.useState(false);
  const listRef = React.useRef<HTMLDivElement>(null);

  const { data, isLoading } = useCircleChildren(appId, page, true);
  const children = React.useMemo(() => data?.children ?? [], [data]);
  const totalCount = data?.totalCount ?? 0;

  const profilePubkeys = React.useMemo(
    () => children.map((child) => child.requesterPubkey).filter(Boolean),
    [children]
  );
  const { profiles } = useNostrProfiles(profilePubkeys);

  const childIds = React.useMemo(
    () => new Set(children.map((child) => child.appId)),
    [children]
  );
  const [selected, setSelected] = React.useState<Set<number>>(new Set());
  const [isRemovingSelected, setRemovingSelected] = React.useState(false);
  const [isConfirmBulkDeleteOpen, setConfirmBulkDeleteOpen] =
    React.useState(false);

  React.useEffect(() => {
    setSelected((current) => {
      const next = new Set(
        Array.from(current).filter((childId) => childIds.has(childId))
      );
      return next.size === current.size ? current : next;
    });
  }, [childIds]);

  const toggleOne = (childId: number) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(childId)) {
        next.delete(childId);
      } else {
        next.add(childId);
      }
      return next;
    });
  };

  const allSelected = children.length > 0 && selected.size === children.length;
  const someSelected = selected.size > 0 && !allSelected;
  const toggleSelectAll = () => {
    setSelected(allSelected ? new Set() : new Set(childIds));
  };

  const refreshChildren = () =>
    mutate(
      (key) =>
        typeof key === "string" &&
        key.startsWith(`/api/apps/${appId}/circle/children`)
    );

  const handlePageChange = (newPage: number) => {
    setPage(newPage);
    listRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
  };

  const handleDelete = async (childId: number) => {
    setDeleting(true);
    try {
      await request(`/api/apps/${appId}/circle/children/${childId}`, {
        method: "DELETE",
      });
      toast(t("circleWallets.removedToast"));
      setConfirmDeleteChild(null);
      // A delete can empty out the last page — step back a page rather than
      // showing an empty list with pagination still pointing past the end.
      if (children.length === 1 && page > 1) {
        setPage(page - 1);
      } else {
        await refreshChildren();
      }
    } catch (error) {
      handleRequestError(t("circleWallets.errors.remove"), error);
    }
    setDeleting(false);
  };

  const handleRemoveSelected = async () => {
    if (selected.size === 0) {
      return;
    }
    setRemovingSelected(true);
    try {
      const ids = Array.from(selected);
      await Promise.all(
        ids.map((childId) =>
          request(`/api/apps/${appId}/circle/children/${childId}`, {
            method: "DELETE",
          })
        )
      );
      toast(
        t("circleWallets.removedSelectedToast", { count: ids.length })
      );
      setSelected(new Set());
      setConfirmBulkDeleteOpen(false);
      if (ids.length === children.length && page > 1) {
        setPage(page - 1);
      } else {
        await refreshChildren();
      }
    } catch (error) {
      handleRequestError(t("circleWallets.errors.removeSelected"), error);
    }
    setRemovingSelected(false);
  };

  return (
    <div ref={listRef} className="grid min-w-0 gap-4">
      <p className="text-sm text-muted-foreground">
        {t("circleWallets.description")}
      </p>

      {selected.size > 0 && (
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-3 text-sm text-muted-foreground">
            <span>{t("common.selectedCount", { count: selected.size })}</span>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setSelected(new Set())}
            >
              {t("common.clear")}
            </Button>
          </div>
          <LoadingButton
            variant="destructive"
            size="sm"
            loading={isRemovingSelected}
            onClick={() => setConfirmBulkDeleteOpen(true)}
          >
            <Trash2Icon className="size-4" /> {t("common.removeSelected")}
          </LoadingButton>
        </div>
      )}

      {isLoading ? (
        <p className="text-sm text-muted-foreground">{tc("loading")}</p>
      ) : children.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t("circleWallets.empty")}
        </p>
      ) : (
        <div className="min-w-0 rounded-lg border">
          <div className="flex items-center gap-3 px-3 py-2.5">
            <Checkbox
              checked={allSelected ? true : someSelected ? "indeterminate" : false}
              onCheckedChange={toggleSelectAll}
              aria-label={t("common.selectAll")}
            />
            <span className="text-sm font-medium text-muted-foreground">
              {t("common.memberColumn")}
            </span>
          </div>
          <div className="grid min-w-0 gap-1 p-1">
            {children.map((child) => (
              <div
                key={child.appId}
                className={cn(
                  "flex min-w-0 cursor-pointer items-start gap-2 rounded-md border p-2 transition-colors hover:bg-accent/50 sm:items-center sm:gap-3",
                  selected.has(child.appId) && "bg-accent/50"
                )}
                onClick={() => navigate(`/apps/${child.appId}`)}
              >
                <Checkbox
                  checked={selected.has(child.appId)}
                  onCheckedChange={() => toggleOne(child.appId)}
                  onClick={(e) => e.stopPropagation()}
                  aria-label={t("circleWallets.selectWallet")}
                  className="mt-1 shrink-0 sm:mt-0"
                />
                <div className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row sm:items-center">
                  <div className="flex min-w-0 flex-1 items-center gap-3">
                    <NostrProfileRow
                      pubkey={child.requesterPubkey}
                      profile={profiles.get(child.requesterPubkey)}
                      avatarClassName="h-9 w-9"
                      showCopy={false}
                    />
                  </div>
                  <div className="flex shrink-0 items-center justify-between gap-2 sm:justify-end sm:gap-3">
                    <div className="whitespace-nowrap text-sm tabular-nums text-muted-foreground">
                      {(child.balanceMloki / 1000).toLocaleString()} loki
                    </div>
                    <Button
                      variant="ghost"
                      size="icon"
                      title={t("common.remove")}
                      aria-label={t("common.remove")}
                      className="text-muted-foreground hover:text-destructive"
                      onClick={(e) => {
                        e.stopPropagation();
                        setConfirmDeleteChild(child);
                      }}
                    >
                      <Trash2Icon className="size-4" />
                    </Button>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      <CustomPagination
        limit={LIST_CIRCLE_CHILDREN_LIMIT}
        totalCount={totalCount}
        page={page}
        handlePageChange={handlePageChange}
      />

      <AlertDialog
        open={confirmDeleteChild !== null}
        onOpenChange={(open) => !open && setConfirmDeleteChild(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("circleWallets.removeTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("circleWallets.removeDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button
              variant="outline"
              onClick={() => setConfirmDeleteChild(null)}
              disabled={isDeleting}
            >
              {tc("actions.cancel")}
            </Button>
            <LoadingButton
              variant="destructive"
              loading={isDeleting}
              onClick={() =>
                confirmDeleteChild && handleDelete(confirmDeleteChild.appId)
              }
            >
              {t("common.remove")}
            </LoadingButton>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={isConfirmBulkDeleteOpen}
        onOpenChange={setConfirmBulkDeleteOpen}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("circleWallets.removeSelectedTitle", {
                count: selected.size,
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("circleWallets.removeSelectedDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button
              variant="outline"
              onClick={() => setConfirmBulkDeleteOpen(false)}
              disabled={isRemovingSelected}
            >
              {tc("actions.cancel")}
            </Button>
            <LoadingButton
              variant="destructive"
              loading={isRemovingSelected}
              onClick={handleRemoveSelected}
            >
              {t("common.remove")}
            </LoadingButton>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
