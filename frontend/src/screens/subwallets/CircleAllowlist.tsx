import { PlusIcon, Trash2Icon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { MemberPicker } from "src/components/circles/MemberPicker";
import { NostrProfileRow } from "src/components/circles/NostrProfileRow";
import { Alert, AlertDescription } from "src/components/ui/alert";
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
import { LoadingButton } from "src/components/ui/custom/loading-button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "src/components/ui/dialog";
import { CustomPagination } from "src/components/CustomPagination";
import { LIST_CIRCLE_ALLOWLIST_LIMIT } from "src/constants";
import { useApp } from "src/hooks/useApp";
import { useCircleIdentity } from "src/hooks/useCircleIdentity";
import { useNostrProfiles } from "src/hooks/useNostrProfiles";
import { cn } from "src/lib/utils";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

type CircleAllowlistProps = {
  appId: number;
  onFormOpenChange?: (open: boolean) => void;
};

export type CircleAllowlistHandle = {
  openAdd: () => void;
};

// Only ever mounted for an allowlist-policy identity (AppDetails.tsx gates
// on identity.policy) — a following-policy circle's authorization is the
// live following-list check surfaced on CircleIdentityCard, not this table.
// The "Add member" trigger lives in AppDetails' CardHeader (top-right of the
// section, next to the title) and drives this component via `ref`.
export const CircleAllowlist = React.forwardRef<
  CircleAllowlistHandle,
  CircleAllowlistProps
>(function CircleAllowlist({ appId, onFormOpenChange }, ref) {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const id = String(appId);
  const { data: app } = useApp(appId);
  const { data: identity } = useCircleIdentity(app?.circleIdentity?.id);
  const [pubkeys, setPubkeys] = React.useState<string[]>([]);
  const [isLoading, setLoading] = React.useState(false);
  const [page, setPage] = React.useState(1);
  const listRef = React.useRef<HTMLDivElement>(null);

  // The allowlist has no server-side pagination — both write endpoints
  // (PUT replace and the bulk-remove path below) operate on the full list,
  // so the full array has to stay in state either way. Paging is applied
  // client-side purely to keep the rendered rows (and their profile
  // fetches) bounded for circles with a large membership.
  const totalPages = Math.max(
    1,
    Math.ceil(pubkeys.length / LIST_CIRCLE_ALLOWLIST_LIMIT)
  );
  React.useEffect(() => {
    setPage((current) => Math.min(current, totalPages));
  }, [totalPages]);
  const pagedPubkeys = pubkeys.slice(
    (page - 1) * LIST_CIRCLE_ALLOWLIST_LIMIT,
    page * LIST_CIRCLE_ALLOWLIST_LIMIT
  );
  const handlePageChange = (newPage: number) => {
    setPage(newPage);
    setSelected(new Set());
    listRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
  };

  const { profiles } = useNostrProfiles(pagedPubkeys);

  const [selected, setSelected] = React.useState<Set<string>>(new Set());
  const [isRemovingSelected, setRemovingSelected] = React.useState(false);
  const [confirmBulkRemove, setConfirmBulkRemove] = React.useState(false);

  const [confirmRemove, setConfirmRemove] = React.useState<string | null>(
    null
  );
  const [isRemovingOne, setRemovingOne] = React.useState(false);

  const [isAddOpen, setAddOpen] = React.useState(false);
  const [pendingAdd, setPendingAdd] = React.useState<string[]>([]);
  const [isAdding, setAdding] = React.useState(false);

  React.useImperativeHandle(ref, () => ({
    openAdd: () => setAddOpen(true),
  }));

  // Mirrors JITHubAllocations: when the allowlist is empty there's no list
  // to make room for, so the add form is embedded inline instead of behind
  // the modal, and the header's "Add member" button (which only opens that
  // modal) stays hidden while either form is visible.
  const isInlineFormShown = !isLoading && pubkeys.length === 0;
  React.useEffect(() => {
    onFormOpenChange?.(isAddOpen || isInlineFormShown);
  }, [isAddOpen, isInlineFormShown, onFormOpenChange]);

  const loadAllowlist = React.useCallback(async () => {
    if (!id) {return;}
    setLoading(true);
    try {
      const data = await request<{ pubkeys: string[] }>(
        `/api/apps/${id}/circle/allowlist`
      );
      setPubkeys(data?.pubkeys ?? []);
    } catch (error) {
      handleRequestError(t("circleAllowlist.errors.load"), error);
    }
    setLoading(false);
  }, [id, t]);

  React.useEffect(() => {
    loadAllowlist();
  }, [loadAllowlist]);

  // Drop any selected pubkey that's no longer on the list (e.g. after a
  // save) instead of leaving it checked against nothing.
  React.useEffect(() => {
    setSelected((current) => {
      const next = new Set(
        Array.from(current).filter((pk) => pubkeys.includes(pk))
      );
      return next.size === current.size ? current : next;
    });
  }, [pubkeys]);

  const saveAllowlist = async (nextPubkeys: string[]) => {
    if (!id) {return;}
    await request(`/api/apps/${id}/circle/allowlist`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ pubkeys: nextPubkeys }),
    });
    await loadAllowlist();
  };

  const handleAdd = async () => {
    if (pendingAdd.length === 0) {return;}
    setAdding(true);
    try {
      await saveAllowlist(Array.from(new Set([...pubkeys, ...pendingAdd])));
      toast(
        t("circleAllowlist.addedToast", { count: pendingAdd.length })
      );
      setPendingAdd([]);
      setAddOpen(false);
    } catch (error) {
      handleRequestError(t("circleAllowlist.errors.add"), error);
    }
    setAdding(false);
  };

  const handleRemove = async (pubkey: string) => {
    if (!id) {return;}
    setRemovingOne(true);
    try {
      await request(`/api/apps/${id}/circle/allowlist/${pubkey}`, {
        method: "DELETE",
      });
      toast(t("circleAllowlist.removedOneToast"));
      setConfirmRemove(null);
      await loadAllowlist();
    } catch (error) {
      handleRequestError(t("circleAllowlist.errors.removeOne"), error);
    }
    setRemovingOne(false);
  };

  const handleRemoveSelected = async () => {
    if (selected.size === 0) {return;}
    setRemovingSelected(true);
    try {
      const removedCount = selected.size;
      await saveAllowlist(pubkeys.filter((pk) => !selected.has(pk)));
      toast(t("circleAllowlist.removedToast", { count: removedCount }));
      setSelected(new Set());
      setConfirmBulkRemove(false);
    } catch (error) {
      handleRequestError(t("circleAllowlist.errors.removeSelected"), error);
    }
    setRemovingSelected(false);
  };

  const toggleOne = (pubkey: string) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(pubkey)) {
        next.delete(pubkey);
      } else {
        next.add(pubkey);
      }
      return next;
    });
  };

  const allSelected =
    pagedPubkeys.length > 0 &&
    pagedPubkeys.every((pk) => selected.has(pk));
  const someSelected = selected.size > 0 && !allSelected;

  const toggleSelectAll = () => {
    setSelected(allSelected ? new Set() : new Set(pagedPubkeys));
  };

  return (
    <div ref={listRef} className="grid min-w-0 gap-4">
      <p className="text-sm text-muted-foreground">
        {t("circleAllowlist.description")}
      </p>

      {identity && identity.usedByCount > 1 && (
        <Alert>
          <AlertDescription>
            {t("circleAllowlist.sharedWarning", {
              count: identity.usedByCount - 1,
            })}
          </AlertDescription>
        </Alert>
      )}

      <div className="flex items-center justify-between gap-3">
        <div className="text-sm text-muted-foreground">
          {selected.size > 0 && (
            <div className="flex items-center gap-3">
              <span>{t("common.selectedCount", { count: selected.size })}</span>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setSelected(new Set())}
              >
                {t("common.clear")}
              </Button>
            </div>
          )}
        </div>
        <div className="flex items-center gap-2">
          {selected.size > 0 && (
            <LoadingButton
              variant="destructive"
              size="sm"
              loading={isRemovingSelected}
              onClick={() => setConfirmBulkRemove(true)}
            >
              <Trash2Icon className="size-4" /> {t("common.removeSelected")}
            </LoadingButton>
          )}
        </div>
      </div>

      {isLoading ? (
        <p className="text-sm text-muted-foreground">{tc("loading")}</p>
      ) : isInlineFormShown ? (
        <div className="max-w-lg rounded-lg border p-4">
          <h3 className="mb-4 flex items-center gap-2 font-medium">
            <PlusIcon className="size-4 text-muted-foreground" />
            {t("circleAllowlist.addMemberHeading")}
          </h3>
          <div className="grid gap-3">
            <MemberPicker
              ownerPubkeyHex={app?.circleIdentity?.providerPubkey}
              selected={pendingAdd}
              onChange={setPendingAdd}
              excludePubkeys={pubkeys}
            />
            <LoadingButton
              loading={isAdding}
              disabled={pendingAdd.length === 0}
              onClick={handleAdd}
              className="w-fit"
            >
              {pendingAdd.length > 0
                ? t("common.addWithCount", { count: pendingAdd.length })
                : t("common.add")}
            </LoadingButton>
          </div>
        </div>
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
            {pagedPubkeys.map((pk) => (
              <div
                key={pk}
                className={cn(
                  "group flex min-w-0 items-start gap-2 rounded-md px-2 py-2 transition-colors hover:bg-accent sm:items-center sm:gap-3",
                  selected.has(pk) && "bg-accent"
                )}
              >
                <Checkbox
                  checked={selected.has(pk)}
                  onCheckedChange={() => toggleOne(pk)}
                  aria-label={t("common.selectMember")}
                  className="mt-1 shrink-0 sm:mt-0"
                />
                <div className="flex min-w-0 flex-1 flex-col gap-1 sm:flex-row sm:items-center sm:gap-3">
                  <div className="flex min-w-0 flex-1 items-center gap-3">
                    <NostrProfileRow
                      pubkey={pk}
                      profile={profiles.get(pk)}
                      avatarClassName="h-9 w-9"
                    />
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="flex self-end sm:hidden sm:group-hover:flex sm:focus-visible:flex sm:self-auto"
                    onClick={() => setConfirmRemove(pk)}
                  >
                    {t("common.remove")}
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      <CustomPagination
        limit={LIST_CIRCLE_ALLOWLIST_LIMIT}
        totalCount={pubkeys.length}
        page={page}
        handlePageChange={handlePageChange}
      />

      <Dialog
        open={isAddOpen}
        onOpenChange={(open) => {
          setAddOpen(open);
          if (!open) {
            setPendingAdd([]);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("circleAllowlist.addMembersTitle")}</DialogTitle>
          </DialogHeader>
          <MemberPicker
            ownerPubkeyHex={app?.circleIdentity?.providerPubkey}
            selected={pendingAdd}
            onChange={setPendingAdd}
            excludePubkeys={pubkeys}
          />
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setAddOpen(false);
                setPendingAdd([]);
              }}
            >
              {tc("actions.cancel")}
            </Button>
            <LoadingButton
              loading={isAdding}
              disabled={pendingAdd.length === 0}
              onClick={handleAdd}
            >
              {pendingAdd.length > 0
                ? t("common.addWithCount", { count: pendingAdd.length })
                : t("common.add")}
            </LoadingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={confirmRemove !== null}
        onOpenChange={(open) => !open && setConfirmRemove(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("circleAllowlist.removeMemberTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("circleAllowlist.removeMemberDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button
              variant="outline"
              onClick={() => setConfirmRemove(null)}
              disabled={isRemovingOne}
            >
              {tc("actions.cancel")}
            </Button>
            <LoadingButton
              variant="destructive"
              loading={isRemovingOne}
              onClick={() => confirmRemove && handleRemove(confirmRemove)}
            >
              {t("common.remove")}
            </LoadingButton>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={confirmBulkRemove} onOpenChange={setConfirmBulkRemove}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("circleAllowlist.removeSelectedTitle", {
                count: selected.size,
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("circleAllowlist.removeMemberDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button
              variant="outline"
              onClick={() => setConfirmBulkRemove(false)}
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
});
