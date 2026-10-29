import { Trash2Icon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
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
import { Badge } from "src/components/ui/badge";
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
// one but not-yet-instantiated), with each wallet's state, grace countdown,
// dust threshold, freeze and pending rotation (NIP-CW §Lifecycle and
// Deletion). The host's action is "Close": the wallet moves to Closing, pays
// out only, and goes once its grace is over and it holds at most dust; its
// balance is never moved. Balances are polled so that member spending shows
// up here without a manual refresh.
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
      toast(t("circleWallets.closedToast"));
      setConfirmDeleteChild(null);
      // A delete can empty out the last page — step back a page rather than
      // showing an empty list with pagination still pointing past the end.
      if (children.length === 1 && page > 1) {
        setPage(page - 1);
      } else {
        await refreshChildren();
      }
    } catch (error) {
      handleRequestError(t("circleWallets.errors.close"), error);
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
      toast(t("circleWallets.closedSelectedToast", { count: ids.length }));
      setSelected(new Set());
      setConfirmBulkDeleteOpen(false);
      if (ids.length === children.length && page > 1) {
        setPage(page - 1);
      } else {
        await refreshChildren();
      }
    } catch (error) {
      handleRequestError(t("circleWallets.errors.closeSelected"), error);
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
            <Trash2Icon className="size-4" /> {t("circleWallets.closeSelected")}
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
              checked={
                allSelected ? true : someSelected ? "indeterminate" : false
              }
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
                    <CircleWalletStatus child={child} />
                    <div className="whitespace-nowrap text-sm tabular-nums text-muted-foreground">
                      {(child.balanceMloki / 1000).toLocaleString()} loki
                    </div>
                    <Button
                      variant="ghost"
                      size="icon"
                      title={t("circleWallets.close")}
                      aria-label={t("circleWallets.close")}
                      disabled={
                        child.state !== "active" && child.state !== "suspended"
                      }
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
            <AlertDialogTitle>{t("circleWallets.closeTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("circleWallets.closeDescription")}
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
              {t("circleWallets.close")}
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
              {t("circleWallets.closeSelectedTitle", {
                count: selected.size,
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("circleWallets.closeSelectedDescription")}
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
              {t("circleWallets.close")}
            </LoadingButton>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// CircleWalletStatus shows a wallet's state, its grace countdown, its freeze
// and its pending rotation as badges.
export function CircleWalletStatus({ child }: { child: CircleChildBalance }) {
  const { t } = useTranslation("circles");
  // Read once per mount: a countdown to the day needs no ticking clock.
  const [now] = React.useState(() => Math.floor(Date.now() / 1000));
  const graceLeft =
    child.graceEndsAt !== undefined ? child.graceEndsAt - now : undefined;
  return (
    <div className="flex flex-wrap items-center gap-1">
      <Badge
        variant={child.state === "active" ? "outline" : "warning"}
        title={t(`circleWallets.stateHint.${child.state}`)}
      >
        {t(`circleWallets.state.${child.state}`)}
      </Badge>
      {graceLeft !== undefined && child.state !== "active" && (
        <span className="text-xs text-muted-foreground">
          {graceLeft > 0
            ? t("circleWallets.graceLeft", {
                days: Math.ceil(graceLeft / 86400),
              })
            : t("circleWallets.graceOver", {
                dust: (child.dustThresholdMloki / 1000).toLocaleString(),
              })}
        </span>
      )}
      {child.frozen && (
        <Badge variant="destructive" title={t("circleWallets.frozenHint")}>
          {t("circleWallets.frozenBadge")}
        </Badge>
      )}
      {child.pendingRotation && (
        <Badge
          variant="outline"
          title={t("circleWallets.rotationHint", {
            key: child.pendingRotation.connectionPubkey.slice(0, 12),
          })}
        >
          {t("circleWallets.rotationBadge", {
            at: new Date(
              child.pendingRotation.completesAt * 1000
            ).toLocaleString(),
          })}
        </Badge>
      )}
    </div>
  );
}
