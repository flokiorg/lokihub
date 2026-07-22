import {
  ChevronDownIcon,
  ChevronRightIcon,
  KeyRound,
  PlusCircleIcon,
  PlusIcon,
  QrCodeIcon,
  Trash2Icon,
  XIcon,
} from "lucide-react";
import { TFunction } from "i18next";
import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { ClaimStateBadge } from "src/components/circles/ClaimStateBadge";
import { NostrProfileRow } from "src/components/circles/NostrProfileRow";
import { NostrPubkeyInput } from "src/components/circles/NostrPubkeyInput";
import { CurrencyInput } from "src/components/CurrencyInput";
import { DurationInput } from "src/components/DurationInput";
import { NostrAvatar } from "src/components/NostrAvatar";
import { RevealConnectionDialog } from "src/components/connections/RevealConnectionDialog";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "src/components/ui/alert-dialog";
import { Avatar, AvatarFallback } from "src/components/ui/avatar";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import { Checkbox } from "src/components/ui/checkbox";
import { CustomPagination } from "src/components/CustomPagination";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "src/components/ui/dialog";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Tabs, TabsList, TabsTrigger } from "src/components/ui/tabs";
import { LIST_JIT_ALLOCATIONS_LIMIT } from "src/constants";
import { useApp } from "src/hooks/useApp";
import { NostrProfile, useNostrProfiles } from "src/hooks/useNostrProfiles";
import { useInputUnit, useUnit } from "src/hooks/useUnit";
import { cn } from "src/lib/utils";
import { formatClaimDeadline } from "src/utils/jitWallet";
import { shortenMiddle } from "src/utils/nostr";
import {
  App,
  CreateJITWalletResponse,
  JITAllocationStatus,
  JITWalletClaim,
  JITWalletClaimCounts,
  ListJITWalletClaimsResponse,
} from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

type JITHubAllocationsProps = {
  appId: number;
  onFormOpenChange?: (open: boolean) => void;
};

export type JITHubAllocationsHandle = {
  openAdd: () => void;
};

// How many recipient avatars a multi-recipient wallet's collapsed summary
// row shows before collapsing the rest into a "+N" count — keeps the row a
// single line regardless of how many beneficiaries (up to
// maxRecipientsPerWallet) share the wallet.
const maxVisibleAvatars = 5;

let recipientRowCounter = 0;
function newRecipientRow(amountLoki: number): RecipientRow {
  recipientRowCounter += 1;
  return {
    key: `r${recipientRowCounter}`,
    identityType: "pubkey",
    pubkeyValue: "",
    resolvedPubkeyHex: undefined,
    connectionKeyValue: "",
    iaPubkeyValue: "",
    amountLoki,
  };
}

type RecipientRow = {
  key: string;
  identityType: "pubkey" | "connection_key";
  pubkeyValue: string;
  resolvedPubkeyHex?: string;
  connectionKeyValue: string;
  iaPubkeyValue: string;
  amountLoki: number;
};

function recipientIdentityValue(row: RecipientRow): string | undefined {
  const value =
    row.identityType === "pubkey"
      ? row.resolvedPubkeyHex
      : row.connectionKeyValue.trim();
  return value || undefined;
}

// Nostr display names are user-supplied and unbounded — a long one must not
// be allowed to eat the whole summary line, or the "& N others" suffix that
// makes the line meaningful gets silently clipped off by the container's
// CSS truncate before a reader ever sees it.
const maxNameLabelLen = 20;

function truncateLabel(label: string, maxLen: number): string {
  return label.length > maxLen ? `${label.slice(0, maxLen - 1)}…` : label;
}

// A short display label for one recipient — resolved profile name/nip05 for
// a pubkey identity, a generic label for a bare connection key (it has no
// profile to resolve). Used to give a multi-recipient wallet's collapsed
// summary row a real name instead of just an avatar stack.
function recipientLabel(
  claim: JITWalletClaim,
  profiles: Map<string, NostrProfile>,
  t: TFunction<"circles">
): string {
  if (claim.identity_type !== "pubkey") {
    return t("identityType.connectionKey");
  }
  const profile = profiles.get(claim.identity_value);
  const name = profile?.displayName || profile?.name || t("common.anonymous");
  return truncateLabel(name, maxNameLabelLen);
}

// Summarizes a wallet's recipients the way an email client lists thread
// participants ("Alice & Bob", "Alice & 2 others") — gives the collapsed
// summary row a name-shaped headline instead of leaving that slot empty,
// which is what made it read as a different kind of row than a
// single-recipient one. Each name is pre-truncated (see recipientLabel)
// rather than relying solely on the container's CSS truncate, so the "& …"
// suffix stays visible instead of being swallowed by one long first name.
function summarizeParticipants(
  claims: JITWalletClaim[],
  profiles: Map<string, NostrProfile>,
  t: TFunction<"circles">
): string {
  const first = recipientLabel(claims[0], profiles, t);
  if (claims.length === 2) {
    return t("jitHubAllocations.participantsPair", {
      first,
      second: recipientLabel(claims[1], profiles, t),
    });
  }
  return t("jitHubAllocations.participantsOthers", {
    first,
    count: claims.length - 1,
  });
}

function formatDurationLabel(
  seconds: number | undefined,
  t: TFunction<"circles">
): string | undefined {
  if (!seconds) {
    return undefined;
  }
  if (seconds % 86400 === 0) {
    return t("jitHubAllocations.durationDays", { count: seconds / 86400 });
  }
  if (seconds % 3600 === 0) {
    return t("jitHubAllocations.durationHours", { count: seconds / 3600 });
  }
  return t("jitHubAllocations.durationMinutes", {
    count: Math.round(seconds / 60),
  });
}

export const JITHubAllocations = React.forwardRef<
  JITHubAllocationsHandle,
  JITHubAllocationsProps
>(function JITHubAllocations({ appId, onFormOpenChange }, ref) {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const id = String(appId);
  const navigate = useNavigate();
  const { data: hub } = useApp(appId);
  const [claims, setClaims] = React.useState<JITWalletClaim[]>([]);
  const [totalCount, setTotalCount] = React.useState(0);
  const [counts, setCounts] = React.useState<JITWalletClaimCounts>({
    all: 0,
    unclaimed: 0,
    claimed: 0,
    expired: 0,
  });
  // "" means the "All" tab - kept distinct from JITAllocationStatus so the
  // query param can be omitted rather than sent as an empty string.
  const [status, setStatus] = React.useState<JITAllocationStatus | "">("");
  const [page, setPage] = React.useState(1);
  const [isLoading, setLoading] = React.useState(false);
  const listRef = React.useRef<HTMLDivElement>(null);

  const pubkeyIdentities = React.useMemo(
    () =>
      claims
        .filter((c) => c.identity_type === "pubkey")
        .map((c) => c.identity_value),
    [claims]
  );
  const { profiles } = useNostrProfiles(pubkeyIdentities);

  // A JIT wallet's connection is deterministically re-derivable (see
  // GetJITWalletConnection on the backend), so it can be revealed inline
  // here without navigating away to the wallet's own AppDetails page. Keyed
  // by wallet_app_id, not the claim id — several claim rows can share the
  // same wallet/connection.
  const [revealApp, setRevealApp] = React.useState<App | undefined>(undefined);
  const [revealUri, setRevealUri] = React.useState<string | undefined>(
    undefined
  );
  // "create" for a wallet just created (not yet connected — shows the
  // waiting-for-connection UX); "reveal" for re-showing an existing wallet's
  // secret.
  const [revealMode, setRevealMode] = React.useState<"reveal" | "create">(
    "reveal"
  );
  const [revealingWalletId, setRevealingWalletId] = React.useState<
    number | null
  >(null);

  const handleRevealConnection = async (walletAppId: number) => {
    setRevealingWalletId(walletAppId);
    try {
      const [revealedApp, connection] = await Promise.all([
        request<App>(`/api/apps/${walletAppId}`),
        request<{ pairing_uri: string }>(
          `/api/apps/${walletAppId}/jit-connection`
        ),
      ]);
      if (revealedApp && connection) {
        setRevealApp(revealedApp);
        setRevealUri(connection.pairing_uri);
        setRevealMode("reveal");
      }
    } catch (error) {
      handleRequestError(t("jitHubAllocations.errors.loadConnection"), error);
    }
    setRevealingWalletId(null);
  };

  // Only unclaimed rows can be individually removed/bulk-selected (removing
  // a claimed slice would mean money already paid out — there's nothing left
  // to reclaim per-slice). A claimed row's only removal path is deleting its
  // whole wallet, which may affect other still-unclaimed recipients sharing
  // the same connection — offered as a separate, single-row action below,
  // not through bulk-select.
  const removableIds = React.useMemo(
    () => new Set(claims.filter((c) => !c.claimed).map((c) => c.id)),
    [claims]
  );
  const [selected, setSelected] = React.useState<Set<number>>(new Set());
  const [isRemovingSelected, setRemovingSelected] = React.useState(false);
  const [confirmDeleteClaim, setConfirmDeleteClaim] =
    React.useState<JITWalletClaim | null>(null);
  const [confirmDeleteWallet, setConfirmDeleteWallet] =
    React.useState<JITWalletClaim | null>(null);
  const [isConfirmBulkDeleteOpen, setConfirmBulkDeleteOpen] =
    React.useState(false);

  React.useEffect(() => {
    setSelected((current) => {
      const next = new Set(
        Array.from(current).filter((claimId) => removableIds.has(claimId))
      );
      return next.size === current.size ? current : next;
    });
  }, [removableIds]);

  const toggleOne = (claimId: number) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(claimId)) {
        next.delete(claimId);
      } else {
        next.add(claimId);
      }
      return next;
    });
  };

  const allSelected =
    removableIds.size > 0 && selected.size === removableIds.size;
  const someSelected = selected.size > 0 && !allSelected;
  const toggleSelectAll = () => {
    setSelected(allSelected ? new Set() : new Set(removableIds));
  };

  // One JIT wallet (one NWC connection) is always exactly one row in the
  // list below — a shared wallet's several beneficiaries never appear as
  // separate top-level rows. For a single-recipient wallet that row already
  // shows everything there is to manage. For a multi-recipient wallet it's a
  // collapsed summary by default; expanding it (tracked here, per
  // wallet_app_id) reveals the per-beneficiary breakdown underneath, which is
  // where individual removal/selection happens.
  const [expandedWallets, setExpandedWallets] = React.useState<Set<number>>(
    new Set()
  );
  const toggleExpanded = (walletAppId: number) => {
    setExpandedWallets((current) => {
      const next = new Set(current);
      if (next.has(walletAppId)) {
        next.delete(walletAppId);
      } else {
        next.add(walletAppId);
      }
      return next;
    });
  };

  const maxAmountLoki = hub?.jitPerWalletMaxMloki
    ? hub.jitPerWalletMaxMloki / 1000
    : undefined;
  const jitMaxExpSecs = hub?.jitMaxExpSecs;

  // Add-form state: one row per recipient, all sharing one identity-type
  // default and one expiry field below. Mixing pubkey and connection_key
  // recipients within the same wallet is allowed — each row picks its own.
  const [isFormOpen, setFormOpen] = React.useState(false);
  const [recipients, setRecipients] = React.useState<RecipientRow[]>([]);
  const [isAdding, setAdding] = React.useState(false);

  const { scaleInputAmount, parseInputAmount } = useUnit();
  const [inputUnit, setInputUnit] = useInputUnit(maxAmountLoki);

  // Duration (seconds) after creation within which every recipient must
  // claim their slice — 0 means no deadline. hasDeadline toggles the input
  // on/off. Shared by the whole wallet (all recipients), not per-row.
  const [hasDeadline, setHasDeadline] = React.useState(false);
  const [claimDeadlineSecs, setClaimDeadlineSecs] = React.useState(86400);

  const resetForm = React.useCallback(() => {
    setRecipients([newRecipientRow(maxAmountLoki ?? 0)]);
    setHasDeadline(false);
    setClaimDeadlineSecs(86400);
  }, [maxAmountLoki]);

  React.useImperativeHandle(ref, () => ({
    openAdd: () => {
      resetForm();
      setFormOpen(true);
    },
  }));

  // The header's "Add JIT wallet" button should stay hidden whenever an add
  // form is already visible — either the modal (isFormOpen) or the inline
  // form that replaces the empty-state message when there's nothing to list
  // yet. Gated on counts.all (the hub's true total, unaffected by the status
  // filter) rather than claims.length, so filtering to e.g. "Expired" with no
  // matches doesn't show the create form — that empty state is "nothing
  // matches this filter", not "this hub has nothing yet".
  const isInlineFormShown = !isLoading && counts.all === 0;
  React.useEffect(() => {
    onFormOpenChange?.(isFormOpen || isInlineFormShown);
  }, [isFormOpen, isInlineFormShown, onFormOpenChange]);
  React.useEffect(() => {
    if (isInlineFormShown && recipients.length === 0) {
      resetForm();
    }
  }, [isInlineFormShown, recipients.length, resetForm]);

  const deadlineExceedsMax =
    hasDeadline &&
    jitMaxExpSecs !== undefined &&
    claimDeadlineSecs > jitMaxExpSecs;

  const totalRequestedLoki = recipients.reduce(
    (sum, r) => sum + r.amountLoki,
    0
  );
  const totalExceedsMax =
    maxAmountLoki !== undefined && totalRequestedLoki > maxAmountLoki;

  const updateRow = (key: string, patch: Partial<RecipientRow>) => {
    setRecipients((rows) =>
      rows.map((r) => (r.key === key ? { ...r, ...patch } : r))
    );
  };
  const removeRow = (key: string) => {
    setRecipients((rows) => rows.filter((r) => r.key !== key));
  };
  const addRow = () => {
    setRecipients((rows) => [...rows, newRecipientRow(0)]);
  };

  const allRowsValid =
    recipients.length > 0 &&
    recipients.every((r) => {
      const identityValue = recipientIdentityValue(r);
      if (!identityValue || r.amountLoki <= 0) {
        return false;
      }
      if (r.identityType === "connection_key" && !r.iaPubkeyValue.trim()) {
        return false;
      }
      return true;
    });

  // Caught client-side (mirroring the backend's own dedupe check in
  // jitwallet.Resolve) so a copy-pasted identity across two rows fails fast
  // with a message pointing at the exact row, instead of a generic toast
  // referencing a "recipient N" the admin has no way to map back to a row —
  // rows aren't otherwise numbered in this form.
  const dedupeKeyCounts = React.useMemo(() => {
    const counts = new Map<string, number>();
    for (const r of recipients) {
      const identityValue = recipientIdentityValue(r);
      if (!identityValue) {
        continue;
      }
      const dedupeKey = `${r.identityType}:${identityValue}`;
      counts.set(dedupeKey, (counts.get(dedupeKey) ?? 0) + 1);
    }
    return counts;
  }, [recipients]);
  const hasDuplicateIdentities = Array.from(dedupeKeyCounts.values()).some(
    (count) => count > 1
  );
  const isDuplicateRow = (row: RecipientRow) => {
    const identityValue = recipientIdentityValue(row);
    if (!identityValue) {
      return false;
    }
    return (
      (dedupeKeyCounts.get(`${row.identityType}:${identityValue}`) ?? 0) > 1
    );
  };

  // `silent` skips the loading flag for background polling refreshes, so
  // claim status updates (mirroring TransactionsList's poll) don't flash
  // the list back to the "Loading…" state every few seconds.
  const loadClaims = React.useCallback(
    async (silent = false) => {
      if (!id) {
        return;
      }
      if (!silent) {
        setLoading(true);
      }
      try {
        const offset = (page - 1) * LIST_JIT_ALLOCATIONS_LIMIT;
        const statusParam = status ? `&status=${status}` : "";
        const data = await request<ListJITWalletClaimsResponse>(
          `/api/apps/${id}/jit-wallets?limit=${LIST_JIT_ALLOCATIONS_LIMIT}&offset=${offset}${statusParam}`
        );
        setClaims(data?.claims ?? []);
        setTotalCount(data?.totalCount ?? 0);
        if (data?.counts) {
          setCounts(data.counts);
        }
      } catch (error) {
        handleRequestError(t("jitHubAllocations.errors.load"), error);
      }
      if (!silent) {
        setLoading(false);
      }
    },
    [id, page, status, t]
  );

  // Switching tabs changes the underlying filtered set, so the current page
  // number (and any row selection, since rows on the old page may not exist
  // in the new filter) no longer applies.
  const handleStatusChange = (next: JITAllocationStatus | "") => {
    setStatus(next);
    setPage(1);
    setSelected(new Set());
  };

  React.useEffect(() => {
    loadClaims();
  }, [loadClaims]);

  const handlePageChange = (newPage: number) => {
    setPage(newPage);
    listRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
  };

  React.useEffect(() => {
    const interval = setInterval(() => loadClaims(true), 3000);
    return () => clearInterval(interval);
  }, [loadClaims]);

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!id || !allRowsValid) {
      return;
    }
    setAdding(true);
    try {
      const body = {
        recipients: recipients.map((r) => ({
          identity_type: r.identityType,
          identity_value: recipientIdentityValue(r),
          ...(r.identityType === "connection_key"
            ? { ia_pubkey: r.iaPubkeyValue.trim() }
            : {}),
          amount_mloki: r.amountLoki * 1000,
        })),
        ...(hasDeadline ? { expiry_secs: claimDeadlineSecs } : {}),
      };
      const result = await request<CreateJITWalletResponse>(
        `/api/apps/${id}/jit-wallets`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }
      );
      toast(
        t("jitHubAllocations.createdToast", { count: recipients.length })
      );
      if (result) {
        const createdApp = await request<App>(`/api/apps/${result.app_id}`);
        if (createdApp) {
          setRevealApp(createdApp);
          setRevealUri(result.pairing_uri);
          setRevealMode("create");
        }
      }
      resetForm();
      setFormOpen(false);
      await loadClaims();
    } catch (error) {
      handleRequestError(t("jitHubAllocations.errors.create"), error);
    }
    setAdding(false);
  };

  const [isDeletingSingle, setDeletingSingle] = React.useState(false);

  const handleDeleteClaim = async (c: JITWalletClaim) => {
    if (!id) {
      return;
    }
    setDeletingSingle(true);
    try {
      await request(
        `/api/apps/${id}/jit-wallets/${c.wallet_app_id}/claims/${c.id}`,
        {
          method: "DELETE",
        }
      );
      toast(t("jitHubAllocations.recipientRemovedToast"));
      setConfirmDeleteClaim(null);
      if (claims.length === 1 && page > 1) {
        setPage(page - 1);
      } else {
        await loadClaims();
      }
    } catch (error) {
      handleRequestError(t("jitHubAllocations.errors.removeRecipient"), error);
    }
    setDeletingSingle(false);
  };

  const handleDeleteWallet = async (c: JITWalletClaim) => {
    if (!id) {
      return;
    }
    setDeletingSingle(true);
    try {
      await request(`/api/apps/${id}/jit-wallets/${c.wallet_app_id}`, {
        method: "DELETE",
      });
      toast(t("jitHubAllocations.walletRemovedToast"));
      setConfirmDeleteWallet(null);
      if (claims.length === 1 && page > 1) {
        setPage(page - 1);
      } else {
        await loadClaims();
      }
    } catch (error) {
      handleRequestError(t("jitHubAllocations.errors.removeWallet"), error);
    }
    setDeletingSingle(false);
  };

  const handleRemoveSelected = async () => {
    if (!id || selected.size === 0) {
      return;
    }
    setRemovingSelected(true);
    try {
      const rows = claims.filter((c) => selected.has(c.id));
      await Promise.all(
        rows.map((c) =>
          request(
            `/api/apps/${id}/jit-wallets/${c.wallet_app_id}/claims/${c.id}`,
            {
              method: "DELETE",
            }
          )
        )
      );
      toast(
        t("jitHubAllocations.recipientsRemovedToast", { count: rows.length })
      );
      setSelected(new Set());
      setConfirmBulkDeleteOpen(false);
      if (rows.length === claims.length && page > 1) {
        setPage(page - 1);
      } else {
        await loadClaims();
      }
    } catch (error) {
      handleRequestError(t("jitHubAllocations.errors.removeSelected"), error);
    }
    setRemovingSelected(false);
  };

  const totalAllocatedLoki = claims.reduce(
    (sum, c) => sum + c.amount_mloki / 1000,
    0
  );

  // One JIT wallet child (one NWC connection) can serve several
  // beneficiaries sharing a single funded pool — grouping the flat claims
  // page by wallet_app_id here shows one row per wallet (with its
  // beneficiaries nested inside) instead of repeating the same "Reveal NWC
  // connection" wallet-level action once per beneficiary. Order follows each
  // wallet's first appearance in `claims` (already newest-first from the
  // API); a wallet's beneficiaries can, in principle, straddle a page
  // boundary — grouping only ever affects rows already present on this page.
  const walletGroups = React.useMemo(() => {
    const order: number[] = [];
    const byWallet = new Map<number, JITWalletClaim[]>();
    for (const c of claims) {
      if (!byWallet.has(c.wallet_app_id)) {
        order.push(c.wallet_app_id);
        byWallet.set(c.wallet_app_id, []);
      }
      byWallet.get(c.wallet_app_id)!.push(c);
    }
    return order.map((walletAppId) => ({
      walletAppId,
      claims: byWallet.get(walletAppId)!,
    }));
  }, [claims]);

  // A status filter only ever shows the subset of a wallet's beneficiaries
  // matching that status — grouping here would display a misleading partial
  // aggregate (e.g. a wallet reading "0/1 claimed" because its one other,
  // already-claimed beneficiary was filtered out of view entirely, not
  // because the wallet only ever had one recipient). Wallet grouping is only
  // meaningful when the full membership is visible, i.e. the unfiltered
  // "All" tab — every other tab lists claims flat, one row per beneficiary,
  // even when several of them happen to share a wallet.
  const displayGroups = React.useMemo(() => {
    if (status === "") {
      return walletGroups;
    }
    return claims.map((c) => ({ walletAppId: c.wallet_app_id, claims: [c] }));
  }, [status, claims, walletGroups]);

  const statusTabs: {
    value: JITAllocationStatus | "";
    label: string;
    count: number;
  }[] = [
    { value: "", label: t("jitHubAllocations.statusAll"), count: counts.all },
    {
      value: "unclaimed",
      label: t("claimBadge.unclaimed"),
      count: counts.unclaimed,
    },
    {
      value: "claimed",
      label: t("claimBadge.claimed"),
      count: counts.claimed,
    },
    {
      value: "expired",
      label: t("jitHubAllocations.statusExpired"),
      count: counts.expired,
    },
  ];

  const recipientRowFields = (row: RecipientRow) => (
    <div key={row.key} className="grid gap-2 rounded-md border p-3">
      <div className="flex items-center justify-between gap-2">
        <Tabs
          value={row.identityType}
          onValueChange={(v) =>
            updateRow(row.key, {
              identityType: v as "pubkey" | "connection_key",
            })
          }
        >
          <TabsList>
            <TabsTrigger value="pubkey">
              {t("identityType.pubkey")}
            </TabsTrigger>
            <TabsTrigger value="connection_key">
              {t("identityType.connectionKey")}
            </TabsTrigger>
          </TabsList>
        </Tabs>
        {recipients.length > 1 && (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            title={t("jitHubAllocations.removeRecipient")}
            aria-label={t("jitHubAllocations.removeRecipient")}
            className="text-muted-foreground hover:text-destructive"
            onClick={() => removeRow(row.key)}
          >
            <Trash2Icon className="size-4" />
          </Button>
        )}
      </div>
      {row.identityType === "pubkey" ? (
        <NostrPubkeyInput
          id={`identityValue-${row.key}`}
          value={row.pubkeyValue}
          onChange={(v) => updateRow(row.key, { pubkeyValue: v })}
          onResolved={(v) => updateRow(row.key, { resolvedPubkeyHex: v })}
          label={t("jitHubAllocations.pubkeyLabel")}
          helperText={t("jitHubAllocations.pubkeyHelper")}
        />
      ) : (
        <>
          <div className="grid gap-1.5">
            <Label htmlFor={`identityValue-${row.key}`}>
              {t("jitHubAllocations.connectionKeyLabel")}
            </Label>
            <Input
              id={`identityValue-${row.key}`}
              type="text"
              placeholder={t("common.hexPlaceholder")}
              value={row.connectionKeyValue}
              onChange={(e) =>
                updateRow(row.key, { connectionKeyValue: e.target.value })
              }
              required
              autoComplete="off"
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor={`iaPubkey-${row.key}`}>
              {t("jitHubAllocations.iaPubkeyLabel")}
            </Label>
            <Input
              id={`iaPubkey-${row.key}`}
              type="text"
              placeholder={t("common.hexPlaceholder")}
              value={row.iaPubkeyValue}
              onChange={(e) =>
                updateRow(row.key, { iaPubkeyValue: e.target.value })
              }
              required
              autoComplete="off"
            />
            <p className="text-sm text-muted-foreground">
              {t("jitHubAllocations.iaPubkeyHelper")}
            </p>
          </div>
        </>
      )}
      {isDuplicateRow(row) && (
        <p className="text-sm text-destructive">
          {t("jitHubAllocations.duplicateIdentity")}
        </p>
      )}
      <div className="grid gap-1.5">
        <Label htmlFor={`amount-${row.key}`}>
          {t("jitHubAllocations.walletBudgetLabel")}
        </Label>
        <CurrencyInput
          id={`amount-${row.key}`}
          amount={
            row.amountLoki
              ? scaleInputAmount(row.amountLoki, inputUnit).toString()
              : ""
          }
          onAmountChange={(val) =>
            updateRow(row.key, {
              amountLoki: parseInputAmount(parseFloat(val) || 0, inputUnit),
            })
          }
          inputUnit={inputUnit}
          onInputUnitChange={setInputUnit}
          required
          min={1}
        />
      </div>
    </div>
  );

  const formFields = (
    <>
      <div className="grid gap-3">{recipients.map(recipientRowFields)}</div>
      <p
        className={cn(
          "text-sm",
          totalExceedsMax ? "text-destructive" : "text-muted-foreground"
        )}
      >
        {maxAmountLoki !== undefined
          ? t("jitHubAllocations.requestedSummaryWithMax", {
              total: totalRequestedLoki.toLocaleString(),
              max: maxAmountLoki.toLocaleString(),
            })
          : t("jitHubAllocations.requestedSummary", {
              total: totalRequestedLoki.toLocaleString(),
            })}
      </p>
      {hasDeadline && (
        <div className="grid gap-1.5">
          <div className="flex items-center justify-between">
            <Label>{t("jitHubAllocations.claimDeadlineLabel")}</Label>
            <XIcon
              className="size-4 cursor-pointer text-muted-foreground"
              onClick={() => setHasDeadline(false)}
            />
          </div>
          <DurationInput
            seconds={claimDeadlineSecs}
            onChange={setClaimDeadlineSecs}
            max={jitMaxExpSecs}
          />
          <p
            className={cn(
              "text-sm",
              deadlineExceedsMax ? "text-destructive" : "text-muted-foreground"
            )}
          >
            {deadlineExceedsMax
              ? t("jitHubAllocations.deadlineExceeds", {
                  max: formatDurationLabel(jitMaxExpSecs, t),
                })
              : formatDurationLabel(jitMaxExpSecs, t)
                ? t("jitHubAllocations.deadlineHelpWithMax", {
                    max: formatDurationLabel(jitMaxExpSecs, t),
                  })
                : t("jitHubAllocations.deadlineHelp")}
          </p>
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          variant="secondary"
          className="w-fit"
          onClick={addRow}
        >
          <PlusCircleIcon /> {t("jitHubAllocations.addRecipient")}
        </Button>
        {!hasDeadline && (
          <Button
            type="button"
            variant="secondary"
            className="w-fit"
            onClick={() => {
              setClaimDeadlineSecs(jitMaxExpSecs || claimDeadlineSecs);
              setHasDeadline(true);
            }}
          >
            <PlusCircleIcon /> {t("jitHubAllocations.setClaimDeadline")}
          </Button>
        )}
      </div>
    </>
  );

  const submitDisabled =
    !allRowsValid ||
    totalExceedsMax ||
    deadlineExceedsMax ||
    hasDuplicateIdentities;

  return (
    <div ref={listRef} className="grid gap-4 min-w-0">
      {!isInlineFormShown && (
        // min-w-0 lets this shrink below the tabs' natural content width
        // instead of forcing the whole page wider. TabsList itself (not
        // this div) owns the actual horizontal scroll if it still doesn't
        // fit — a second overflow-x-auto here would create two independent
        // scroll positions for the same content, where scrolling one back
        // to the start doesn't reset the other, leaving the first tab
        // stuck partly offscreen.
        <div className="min-w-0">
          <Tabs
            value={status}
            onValueChange={(v) =>
              handleStatusChange(v as JITAllocationStatus | "")
            }
          >
            <TabsList>
              {statusTabs.map((tab) => (
                <TabsTrigger
                  key={tab.value || "all"}
                  value={tab.value}
                  className="shrink-0 gap-1.5"
                >
                  {tab.label}
                  <Badge
                    variant={
                      tab.value === "expired" && tab.count > 0
                        ? "warning"
                        : "secondary"
                    }
                    className="px-1.5 py-0 text-[11px] font-normal tabular-nums"
                  >
                    {tab.count}
                  </Badge>
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
        </div>
      )}

      {claims.length > 0 && (
        <p className="text-sm text-muted-foreground">
          {t("jitHubAllocations.allocatedSummary", {
            allocated: totalAllocatedLoki.toLocaleString(),
            total: totalCount.toLocaleString(),
            status: status
              ? (statusTabs.find((tab) => tab.value === status)?.label ??
                status)
              : t("common.total"),
          })}
        </p>
      )}

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
      ) : claims.length > 0 ? (
        <div className="rounded-lg border min-w-0 overflow-x-auto">
          <div className="flex items-center gap-3 px-3 py-2.5">
            <Checkbox
              checked={
                allSelected ? true : someSelected ? "indeterminate" : false
              }
              onCheckedChange={toggleSelectAll}
              disabled={removableIds.size === 0}
              aria-label={t("common.selectAll")}
            />
            <span className="text-sm font-medium text-muted-foreground">
              {t("jitHubAllocations.recipientColumn")}
            </span>
          </div>
          <div className="grid gap-2 p-1 min-w-0">
            {displayGroups.map((group) => {
              const isMulti = group.claims.length > 1;
              // Deadline/expiry is one property of the wallet App itself,
              // shared by every beneficiary in the group — safe to read off
              // the first claim.
              const deadline = group.claims[0].expires_at
                ? formatClaimDeadline(group.claims[0].expires_at)
                : undefined;

              // A single-recipient wallet's one row already shows everything
              // there is to manage for it — render it exactly like a plain
              // recipient row, reveal action included, and skip the
              // summary/expand machinery entirely.
              if (!isMulti) {
                const c = group.claims[0];
                const canRemoveRow = removableIds.has(c.id);
                return (
                  <div
                    key={group.claims[0].id}
                    className={cn(
                      "group flex min-w-0 cursor-pointer items-start gap-2 rounded-md border p-2 transition-colors hover:bg-accent/50 sm:items-center sm:gap-3",
                      selected.has(c.id) && "bg-accent/50"
                    )}
                    onClick={() => navigate(`/apps/${c.wallet_app_id}`)}
                  >
                    <Checkbox
                      checked={selected.has(c.id)}
                      onCheckedChange={() => toggleOne(c.id)}
                      onClick={(e) => e.stopPropagation()}
                      disabled={!canRemoveRow}
                      aria-label={t("jitHubAllocations.selectRecipient")}
                      className="mt-1 shrink-0 sm:mt-0"
                    />
                    <div className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row sm:items-center">
                      <div className="flex min-w-0 flex-1 items-center gap-3">
                        {c.identity_type === "pubkey" ? (
                          <NostrProfileRow
                            pubkey={c.identity_value}
                            profile={profiles.get(c.identity_value)}
                            avatarClassName="h-9 w-9"
                            showCopy={false}
                          />
                        ) : (
                          <>
                            <Avatar className="h-9 w-9 shrink-0">
                              <AvatarFallback>
                                <KeyRound className="h-4 w-4 text-muted-foreground" />
                              </AvatarFallback>
                            </Avatar>
                            <span className="min-w-0 flex-1">
                              <span className="block truncate font-mono text-xs text-muted-foreground">
                                {shortenMiddle(c.identity_value)}
                              </span>
                              <Badge variant="outline" className="mt-1">
                                {t("identityType.connectionKey")}
                              </Badge>
                            </span>
                          </>
                        )}
                      </div>

                      <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 sm:flex-nowrap sm:justify-end sm:gap-3">
                        <div
                          className="text-end tabular-nums"
                          title={deadline?.title}
                        >
                          <div className="flex items-center justify-end gap-2">
                            <span className="text-sm font-medium">
                              {(c.amount_mloki / 1000).toLocaleString()} loki
                            </span>
                            <ClaimStateBadge claim={c} />
                          </div>
                          <div className="text-xs text-muted-foreground">
                            {deadline?.label ?? t("claimDeadline.none")}
                          </div>
                        </div>

                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("jitHubAllocations.revealConnection")}
                          aria-label={t("jitHubAllocations.revealConnection")}
                          disabled={revealingWalletId === group.walletAppId}
                          onClick={(e) => {
                            e.stopPropagation();
                            handleRevealConnection(group.walletAppId);
                          }}
                        >
                          <QrCodeIcon className="size-4" />
                        </Button>

                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("common.remove")}
                          aria-label={t("common.remove")}
                          className={cn(
                            "text-muted-foreground hover:text-destructive",
                            !canRemoveRow && "invisible"
                          )}
                          disabled={!canRemoveRow}
                          onClick={(e) => {
                            e.stopPropagation();
                            setConfirmDeleteClaim(c);
                          }}
                        >
                          <Trash2Icon className="size-4" />
                        </Button>
                      </div>
                    </div>
                  </div>
                );
              }

              // Multi-recipient wallet: exactly one summary row, collapsed by
              // default — per-beneficiary detail (and the ability to
              // select/remove one of them) only appears once expanded.
              const totalLoki = group.claims.reduce(
                (sum, c) => sum + c.amount_mloki / 1000,
                0
              );
              const claimedCount = group.claims.filter((c) => c.claimed).length;
              const groupRemovableIds = group.claims
                .filter((c) => removableIds.has(c.id))
                .map((c) => c.id);
              const groupSelectedCount = groupRemovableIds.filter((rid) =>
                selected.has(rid)
              ).length;
              const groupAllSelected =
                groupRemovableIds.length > 0 &&
                groupSelectedCount === groupRemovableIds.length;
              const groupSomeSelected =
                groupSelectedCount > 0 && !groupAllSelected;
              const toggleGroupSelected = () => {
                setSelected((current) => {
                  const next = new Set(current);
                  for (const rid of groupRemovableIds) {
                    if (groupAllSelected) {
                      next.delete(rid);
                    } else {
                      next.add(rid);
                    }
                  }
                  return next;
                });
              };
              const isExpanded = expandedWallets.has(group.walletAppId);

              return (
                <div
                  key={group.claims[0].id}
                  className="rounded-md border min-w-0"
                >
                  <div
                    className="flex min-w-0 cursor-pointer items-start gap-2 p-2 transition-colors hover:bg-accent/50 sm:items-center sm:gap-3"
                    onClick={() => navigate(`/apps/${group.walletAppId}`)}
                  >
                    <Checkbox
                      checked={
                        groupAllSelected
                          ? true
                          : groupSomeSelected
                            ? "indeterminate"
                            : false
                      }
                      onCheckedChange={toggleGroupSelected}
                      onClick={(e) => e.stopPropagation()}
                      disabled={groupRemovableIds.length === 0}
                      aria-label={t("jitHubAllocations.selectAllInWallet")}
                      className="mt-1 shrink-0 sm:mt-0"
                    />
                    <div className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row sm:items-center">
                      <div className="flex min-w-0 flex-1 items-center gap-3">
                        <div className="flex -space-x-3 shrink-0">
                          {group.claims.slice(0, maxVisibleAvatars).map((c) =>
                            c.identity_type === "pubkey" ? (
                              <NostrAvatar
                                key={c.id}
                                pubkey={c.identity_value}
                                profile={profiles.get(c.identity_value)}
                                className="h-9 w-9 border-2 border-background"
                              />
                            ) : (
                              <Avatar
                                key={c.id}
                                className="h-9 w-9 border-2 border-background"
                              >
                                <AvatarFallback>
                                  <KeyRound className="h-4 w-4 text-muted-foreground" />
                                </AvatarFallback>
                              </Avatar>
                            )
                          )}
                          {group.claims.length > maxVisibleAvatars && (
                            <span className="z-10 flex h-9 w-9 items-center justify-center rounded-full border-2 border-background bg-muted text-xs text-muted-foreground">
                              +{group.claims.length - maxVisibleAvatars}
                            </span>
                          )}
                        </div>
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-sm font-medium">
                            {summarizeParticipants(group.claims, profiles, t)}
                          </div>
                          <div className="truncate text-xs text-muted-foreground">
                            {t("jitHubAllocations.groupSummary", {
                              count: group.claims.length,
                              claimed: claimedCount,
                            })}
                          </div>
                        </div>
                      </div>

                      <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 sm:flex-nowrap sm:justify-end sm:gap-3">
                        <div
                          className="text-end tabular-nums"
                          title={deadline?.title}
                        >
                          <div className="text-sm font-medium">
                            {totalLoki.toLocaleString()} loki
                          </div>
                          <div className="text-xs text-muted-foreground">
                            {deadline?.label ?? t("claimDeadline.none")}
                          </div>
                        </div>

                        <Button
                          variant="ghost"
                          size="icon"
                          onClick={(e) => {
                            e.stopPropagation();
                            toggleExpanded(group.walletAppId);
                          }}
                          aria-expanded={isExpanded}
                          aria-label={
                            isExpanded
                              ? t("jitHubAllocations.collapseRecipients")
                              : t("jitHubAllocations.expandRecipients")
                          }
                        >
                          {isExpanded ? (
                            <ChevronDownIcon className="size-4 text-muted-foreground" />
                          ) : (
                            <ChevronRightIcon className="size-4 text-muted-foreground" />
                          )}
                        </Button>

                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("jitHubAllocations.revealConnection")}
                          aria-label={t("jitHubAllocations.revealConnection")}
                          disabled={revealingWalletId === group.walletAppId}
                          onClick={(e) => {
                            e.stopPropagation();
                            handleRevealConnection(group.walletAppId);
                          }}
                        >
                          <QrCodeIcon className="size-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("jitHubAllocations.removeWallet")}
                          aria-label={t("jitHubAllocations.removeWallet")}
                          className="text-muted-foreground hover:text-destructive"
                          onClick={(e) => {
                            e.stopPropagation();
                            setConfirmDeleteWallet(group.claims[0]);
                          }}
                        >
                          <Trash2Icon className="size-4" />
                        </Button>
                      </div>
                    </div>
                  </div>

                  {isExpanded && (
                    <div className="grid max-h-96 gap-1 overflow-y-auto overscroll-contain border-t p-1 min-w-0">
                      {group.claims.map((c) => {
                        const canRemoveRow = removableIds.has(c.id);
                        return (
                          <div
                            key={c.id}
                            className={cn(
                              "group flex min-w-0 items-start gap-2 rounded-md p-2 transition-colors hover:bg-accent/50 sm:items-center sm:gap-3",
                              selected.has(c.id) && "bg-accent/50"
                            )}
                          >
                            <Checkbox
                              checked={selected.has(c.id)}
                              onCheckedChange={() => toggleOne(c.id)}
                              disabled={!canRemoveRow}
                              aria-label={t("jitHubAllocations.selectRecipient")}
                              className="mt-1 shrink-0 sm:mt-0"
                            />
                            <div className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row sm:items-center">
                              <div
                                className={cn(
                                  "flex min-w-0 flex-1 items-center gap-3",
                                  c.claimed &&
                                    "cursor-pointer rounded-md hover:opacity-80"
                                )}
                                onClick={() =>
                                  c.claimed &&
                                  navigate(`/apps/${c.wallet_app_id}`)
                                }
                              >
                                {c.identity_type === "pubkey" ? (
                                  <NostrProfileRow
                                    pubkey={c.identity_value}
                                    profile={profiles.get(c.identity_value)}
                                    avatarClassName="h-9 w-9"
                                    showCopy={false}
                                  />
                                ) : (
                                  <>
                                    <Avatar className="h-9 w-9 shrink-0">
                                      <AvatarFallback>
                                        <KeyRound className="h-4 w-4 text-muted-foreground" />
                                      </AvatarFallback>
                                    </Avatar>
                                    <span className="min-w-0 flex-1">
                                      <span className="block truncate font-mono text-xs text-muted-foreground">
                                        {shortenMiddle(c.identity_value)}
                                      </span>
                                      <Badge variant="outline" className="mt-1">
                                        {t("identityType.connectionKey")}
                                      </Badge>
                                    </span>
                                  </>
                                )}
                              </div>

                              <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 sm:flex-nowrap sm:justify-end sm:gap-3">
                                <span className="text-sm font-medium tabular-nums">
                                  {(c.amount_mloki / 1000).toLocaleString()}{" "}
                                  loki
                                </span>
                                <ClaimStateBadge claim={c} />

                                <Button
                                  variant="ghost"
                                  size="icon"
                                  title={t("common.remove")}
                                  aria-label={t("common.remove")}
                                  className={cn(
                                    "text-muted-foreground hover:text-destructive",
                                    !canRemoveRow && "invisible"
                                  )}
                                  disabled={!canRemoveRow}
                                  onClick={() => setConfirmDeleteClaim(c)}
                                >
                                  <Trash2Icon className="size-4" />
                                </Button>
                              </div>
                            </div>
                          </div>
                        );
                      })}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      ) : !isInlineFormShown ? (
        // The hub has claims overall, just none on this page — either this
        // tab's filter has no matches, or (rarer) a stale page number after
        // e.g. a delete. Either way, an empty-filter/page state, not an
        // empty-hub one, so this shouldn't offer to create a wallet.
        <p className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
          {status
            ? t("jitHubAllocations.noneFiltered", {
                status:
                  statusTabs.find((tab) => tab.value === status)?.label ??
                  status,
              })
            : t("jitHubAllocations.noneOnPage")}
        </p>
      ) : (
        <div className="rounded-lg border p-4 max-w-lg">
          <h3 className="mb-4 flex items-center gap-2 font-medium">
            <PlusIcon className="size-4 text-muted-foreground" />
            {t("jitHubAllocations.addHeading")}
          </h3>
          <form onSubmit={handleAdd} className="grid gap-3">
            {formFields}
            <LoadingButton
              loading={isAdding}
              disabled={submitDisabled}
              type="submit"
              className="w-fit"
            >
              {t("common.add")}
            </LoadingButton>
          </form>
        </div>
      )}

      <CustomPagination
        limit={LIST_JIT_ALLOCATIONS_LIMIT}
        totalCount={totalCount}
        page={page}
        handlePageChange={handlePageChange}
      />

      <Dialog
        open={isFormOpen}
        onOpenChange={(open) => {
          setFormOpen(open);
          if (!open) {
            resetForm();
          }
        }}
      >
        <DialogContent className="max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t("jitHubAllocations.createDialogTitle")}</DialogTitle>
          </DialogHeader>
          <form onSubmit={handleAdd} className="grid gap-3">
            {formFields}
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  setFormOpen(false);
                  resetForm();
                }}
              >
                {tc("actions.cancel")}
              </Button>
              <LoadingButton
                loading={isAdding}
                disabled={submitDisabled}
                type="submit"
              >
                {t("common.add")}
              </LoadingButton>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {revealApp && revealUri && (
        <RevealConnectionDialog
          app={revealApp}
          pairingUri={revealUri}
          mode={revealMode}
          onClose={() => {
            setRevealUri(undefined);
            setRevealApp(undefined);
          }}
        />
      )}

      <AlertDialog
        open={confirmDeleteClaim !== null}
        onOpenChange={(open) => !open && setConfirmDeleteClaim(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("jitHubAllocations.removeRecipientTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription asChild>
              <p>{t("jitHubAllocations.removeRecipientDescription")}</p>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button
              variant="outline"
              onClick={() => setConfirmDeleteClaim(null)}
              disabled={isDeletingSingle}
            >
              {tc("actions.cancel")}
            </Button>
            <LoadingButton
              variant="destructive"
              loading={isDeletingSingle}
              onClick={() =>
                confirmDeleteClaim && handleDeleteClaim(confirmDeleteClaim)
              }
            >
              {t("common.remove")}
            </LoadingButton>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={confirmDeleteWallet !== null}
        onOpenChange={(open) => !open && setConfirmDeleteWallet(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("jitHubAllocations.removeWalletTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription asChild>
              <p>{t("jitHubAllocations.removeWalletDescription")}</p>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button
              variant="outline"
              onClick={() => setConfirmDeleteWallet(null)}
              disabled={isDeletingSingle}
            >
              {tc("actions.cancel")}
            </Button>
            <LoadingButton
              variant="destructive"
              loading={isDeletingSingle}
              onClick={() =>
                confirmDeleteWallet && handleDeleteWallet(confirmDeleteWallet)
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
              {t("jitHubAllocations.removeSelectedTitle", {
                count: selected.size,
              })}
            </AlertDialogTitle>
            <AlertDialogDescription asChild>
              <p>{t("jitHubAllocations.removeSelectedDescription")}</p>
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
});
