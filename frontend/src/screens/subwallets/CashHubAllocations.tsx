import {
  BanknoteIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  CoinsIcon,
  KeyRound,
  PlusCircleIcon,
  PlusIcon,
  QrCodeIcon,
  Trash2Icon,
  UserRoundIcon,
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
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "src/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "src/components/ui/tabs";
import { LIST_CASH_ALLOCATIONS_LIMIT } from "src/constants";
import { useApp } from "src/hooks/useApp";
import { useIdentityAuthorities } from "src/hooks/useIdentityAuthorities";
import { useNip05Verification } from "src/hooks/useNip05Verification";
import { NostrProfile, useNostrProfiles } from "src/hooks/useNostrProfiles";
import { useInputUnit, useUnit } from "src/hooks/useUnit";
import { getAuthToken } from "src/lib/auth";
import { copyToClipboard } from "src/lib/clipboard";
import { cn } from "src/lib/utils";
import { formatClaimDeadline } from "src/utils/cashWallet";
import { safeNpubEncode, shortenMiddle } from "src/utils/nostr";
import { validateHTTPURL } from "src/utils/validation";
import {
  App,
  CreateCashWalletResponse,
  CashAllocationStatus,
  CashWalletClaim,
  CashWalletClaimCounts,
  CashWalletConnectionResponse,
  ListCashWalletClaimsResponse,
} from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

type CashHubAllocationsProps = {
  appId: number;
  onFormOpenChange?: (open: boolean) => void;
};

export type CashHubAllocationsHandle = {
  openAdd: () => void;
};

// How many identity pills a row shows before collapsing the rest into a
// "+N" badge — keeps the row a single line regardless of how many
// beneficiaries (up to maxRecipientsPerWallet) share the wallet.
const maxVisiblePills = 5;

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
    // Undefined until the operator picks one explicitly — the render layer
    // defaults it to "existing" whenever any Identity Authority is already
    // declared in Settings, "manual" otherwise (see iaModeFor below).
    iaMode: undefined,
    amountLoki,
  };
}

type RecipientRow = {
  key: string;
  identityType: "pubkey" | "connection_key" | "bearer";
  pubkeyValue: string;
  resolvedPubkeyHex?: string;
  connectionKeyValue: string;
  iaPubkeyValue: string;
  // "existing" shows a Select sourced from Settings' declared Identity
  // Authorities; "manual" shows the free-text pubkey Input. A row can switch
  // between the two any number of times before submission.
  iaMode?: "existing" | "manual";
  amountLoki: number;
};

// __manual__ can never collide with a real hex pubkey (odd length, non-hex
// chars) — used as the Select's sentinel value for "switch to manual entry."
const MANUAL_IA_SENTINEL = "__manual__";

function iaModeFor(
  row: RecipientRow,
  hasSavedIAs: boolean
): "existing" | "manual" {
  return row.iaMode ?? (hasSavedIAs ? "existing" : "manual");
}

// A bearer row has no identity at all — the wallet mints the secret itself
// (NIP-CASH §Bearer Slices) — so there is no identity_value to send for it.
function recipientIdentityValue(row: RecipientRow): string | undefined {
  if (row.identityType === "bearer") {
    return undefined;
  }
  const value =
    row.identityType === "pubkey"
      ? row.resolvedPubkeyHex
      : row.connectionKeyValue.trim();
  return value || undefined;
}

// A pubkey pill's leading glyph: the recipient's own profile picture when
// they have one (proxied through our backend, same as NostrAvatar, so the
// browser never connects straight to an arbitrary Nostr media host), or the
// generic identity icon otherwise — deliberately never initials. A pill is
// meant to read as "an icon plus a short identifier," and initials read as
// a half-resolved name, which contradicts the whole point of not showing
// names here.
function PillIdentityGlyph({ profile }: { profile: NostrProfile | undefined }) {
  const [imageFailed, setImageFailed] = React.useState(false);
  const authToken = getAuthToken();
  const pictureUrl =
    profile?.picture &&
    validateHTTPURL(profile.picture, "profile picture") === null &&
    authToken
      ? `/api/circle/avatar-proxy?url=${encodeURIComponent(profile.picture)}&token=${encodeURIComponent(authToken)}`
      : undefined;

  if (!pictureUrl || imageFailed) {
    return <UserRoundIcon className="h-3 w-3 shrink-0" />;
  }
  return (
    <img
      src={pictureUrl}
      alt=""
      onError={() => setImageFailed(true)}
      className="h-3.5 w-3.5 shrink-0 rounded-full object-cover"
    />
  );
}

// A pubkey pill's text: a verified nip05 reads far better than a hex
// fragment, but an unverified one is just an unchecked claim from the
// profile's own kind:0 event — falls back to a short npub (never raw hex)
// whenever nip05 is absent or hasn't been confirmed to resolve back to this
// pubkey yet.
function pillIdentityLabel(
  pubkey: string,
  profile: NostrProfile | undefined,
  isVerifiedNip05: boolean
): string {
  if (isVerifiedNip05 && profile?.nip05) {
    return profile.nip05;
  }
  return shortenMiddle(safeNpubEncode(pubkey) ?? pubkey, 8, 4);
}

function formatDurationLabel(
  seconds: number | undefined,
  t: TFunction<"circles">
): string | undefined {
  if (!seconds) {
    return undefined;
  }
  if (seconds % 86400 === 0) {
    return t("cashHubAllocations.durationDays", { count: seconds / 86400 });
  }
  if (seconds % 3600 === 0) {
    return t("cashHubAllocations.durationHours", { count: seconds / 3600 });
  }
  return t("cashHubAllocations.durationMinutes", {
    count: Math.round(seconds / 60),
  });
}

export const CashHubAllocations = React.forwardRef<
  CashHubAllocationsHandle,
  CashHubAllocationsProps
>(function CashHubAllocations({ appId, onFormOpenChange }, ref) {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const id = String(appId);
  const navigate = useNavigate();
  const { data: hub } = useApp(appId);
  const [claims, setClaims] = React.useState<CashWalletClaim[]>([]);
  const [totalCount, setTotalCount] = React.useState(0);
  const [counts, setCounts] = React.useState<CashWalletClaimCounts>({
    all: 0,
    unclaimed: 0,
    claimed: 0,
    expired: 0,
  });
  // "" means the "All" tab - kept distinct from CashAllocationStatus so the
  // query param can be omitted rather than sent as an empty string.
  const [status, setStatus] = React.useState<CashAllocationStatus | "">("");
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
  // Verifies each pubkey recipient's claimed nip05 actually resolves back to
  // them (DNS .well-known lookup) before the pill trusts it over a short
  // npub — an unverified nip05 string in a kind:0 event is just a claim.
  const { verified: verifiedNip05 } = useNip05Verification(profiles);

  // Backs the connection_key recipient rows' "pick an already-declared
  // Identity Authority" dropdown, so minting a Web Identity slice doesn't
  // require re-typing an IA pubkey the operator already registered in
  // Settings. Mirrors Services.tsx's own fetch-on-mount usage.
  const { authorities: identityAuthorities, fetchIdentityAuthorities } =
    useIdentityAuthorities();
  React.useEffect(() => {
    fetchIdentityAuthorities();
  }, [fetchIdentityAuthorities]);

  // A Cash wallet's connection is deterministically re-derivable (see
  // GetCashWalletConnection on the backend), so it can be revealed inline
  // here without navigating away to the wallet's own AppDetails page. Keyed
  // by wallet_app_id, not the claim id — several claim rows can share the
  // same wallet/connection.
  const [revealApp, setRevealApp] = React.useState<App | undefined>(undefined);
  const [revealUri, setRevealUri] = React.useState<string | undefined>(
    undefined
  );
  // Companion strings for the same connection — lokicashToken is always
  // derivable alongside pairing_uri (both endpoints return it); bearerSecret
  // is populated only right after creating a bearer-mode wallet, and only
  // that once (NIP-CASH §Bearer Slices: it is never retrievable again).
  const [revealLokicashToken, setRevealLokicashToken] = React.useState<
    string | undefined
  >(undefined);
  const [revealBearerSecret, setRevealBearerSecret] = React.useState<
    string | undefined
  >(undefined);
  // The wallet's totals shown above the QR in the reveal dialog — the
  // caller already has this (either from the just-created response, or from
  // the claims already loaded into this list), so it's captured alongside
  // the connection strings rather than re-fetched.
  const [revealSummary, setRevealSummary] = React.useState<
    | {
        amountLoki: number;
        recipientCount: number;
        claimedCount: number;
        expiresAtSecs?: number;
      }
    | undefined
  >(undefined);
  // "create" for a wallet just created (not yet connected — shows the
  // waiting-for-connection UX); "reveal" for re-showing an existing wallet's
  // secret.
  const [revealMode, setRevealMode] = React.useState<"reveal" | "create">(
    "reveal"
  );
  const [revealingWalletId, setRevealingWalletId] = React.useState<
    number | null
  >(null);

  const handleRevealConnection = async (
    walletAppId: number,
    summary: {
      amountLoki: number;
      recipientCount: number;
      claimedCount: number;
      expiresAtSecs?: number;
    }
  ) => {
    setRevealingWalletId(walletAppId);
    try {
      const [revealedApp, connection] = await Promise.all([
        request<App>(`/api/apps/${walletAppId}`),
        request<CashWalletConnectionResponse>(
          `/api/apps/${walletAppId}/cash-connection`
        ),
      ]);
      if (revealedApp && connection) {
        setRevealApp(revealedApp);
        setRevealUri(connection.pairing_uri);
        setRevealLokicashToken(connection.cash_token);
        setRevealBearerSecret(undefined);
        setRevealSummary(summary);
        setRevealMode("reveal");
      }
    } catch (error) {
      handleRequestError(t("cashHubAllocations.errors.loadConnection"), error);
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
    React.useState<CashWalletClaim | null>(null);
  const [confirmDeleteWallet, setConfirmDeleteWallet] =
    React.useState<CashWalletClaim | null>(null);
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

  // One Cash wallet (one NWC connection) is always exactly one row in the
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

  const maxAmountLoki = hub?.cashPerWalletMaxMloki
    ? hub.cashPerWalletMaxMloki / 1000
    : undefined;
  const cashMaxExpSecs = hub?.cashMaxExpSecs;

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

  // The header's "Add Cash wallet" button should stay hidden whenever an add
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

  // cashMaxExpSecs === 0 means the hub itself has no ceiling ("never") — an
  // explicit per-wallet deadline is honored exactly in that case, so there's
  // nothing to exceed.
  const deadlineExceedsMax =
    hasDeadline && !!cashMaxExpSecs && claimDeadlineSecs > cashMaxExpSecs;

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

  // A bearer recipient has no identity, and MUST be the wallet's only
  // recipient (NIP-CASH §Bearer Slices — a bearer slice never shares a wallet
  // with another recipient, since redeeming one transmits its raw secret in
  // the request body, decryptable by anyone still holding the shared
  // connection). Switching a row to bearer collapses the form down to just
  // that row, rather than leaving now-invalid sibling rows for the admin to
  // notice and remove manually.
  const setRowIdentityType = (
    key: string,
    identityType: RecipientRow["identityType"]
  ) => {
    setRecipients((rows) => {
      if (identityType === "bearer") {
        const row = rows.find((r) => r.key === key);
        return row ? [{ ...row, identityType }] : rows;
      }
      return rows.map((r) => (r.key === key ? { ...r, identityType } : r));
    });
  };
  const hasBearerRow = recipients.some((r) => r.identityType === "bearer");

  const allRowsValid =
    recipients.length > 0 &&
    !(hasBearerRow && recipients.length > 1) &&
    recipients.every((r) => {
      if (r.amountLoki <= 0) {
        return false;
      }
      if (r.identityType === "bearer") {
        return true;
      }
      const identityValue = recipientIdentityValue(r);
      if (!identityValue) {
        return false;
      }
      if (r.identityType === "connection_key" && !r.iaPubkeyValue.trim()) {
        return false;
      }
      return true;
    });

  // Caught client-side (mirroring the backend's own dedupe check in
  // cashwallet.Resolve) so a copy-pasted identity across two rows fails fast
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
        const offset = (page - 1) * LIST_CASH_ALLOCATIONS_LIMIT;
        const statusParam = status ? `&status=${status}` : "";
        const data = await request<ListCashWalletClaimsResponse>(
          `/api/apps/${id}/cash-wallets?limit=${LIST_CASH_ALLOCATIONS_LIMIT}&offset=${offset}${statusParam}`
        );
        setClaims(data?.claims ?? []);
        setTotalCount(data?.totalCount ?? 0);
        if (data?.counts) {
          setCounts(data.counts);
        }
      } catch (error) {
        handleRequestError(t("cashHubAllocations.errors.load"), error);
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
  const handleStatusChange = (next: CashAllocationStatus | "") => {
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
      const result = await request<CreateCashWalletResponse>(
        `/api/apps/${id}/cash-wallets`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }
      );
      toast(t("cashHubAllocations.createdToast", { count: recipients.length }));
      if (result) {
        const createdApp = await request<App>(`/api/apps/${result.app_id}`);
        if (createdApp) {
          setRevealApp(createdApp);
          setRevealUri(result.pairing_uri);
          setRevealLokicashToken(result.cash_token);
          setRevealBearerSecret(result.recipients[0]?.bearer_secret);
          setRevealSummary({
            amountLoki: totalRequestedLoki,
            recipientCount: result.recipients.length,
            claimedCount: 0,
            expiresAtSecs: result.expires_at || undefined,
          });
          setRevealMode("create");
        }
      }
      resetForm();
      setFormOpen(false);
      await loadClaims();
    } catch (error) {
      handleRequestError(t("cashHubAllocations.errors.create"), error);
    }
    setAdding(false);
  };

  const [isDeletingSingle, setDeletingSingle] = React.useState(false);

  const handleDeleteClaim = async (c: CashWalletClaim) => {
    if (!id) {
      return;
    }
    setDeletingSingle(true);
    try {
      await request(
        `/api/apps/${id}/cash-wallets/${c.wallet_app_id}/claims/${c.id}`,
        {
          method: "DELETE",
        }
      );
      toast(t("cashHubAllocations.recipientRemovedToast"));
      setConfirmDeleteClaim(null);
      if (claims.length === 1 && page > 1) {
        setPage(page - 1);
      } else {
        await loadClaims();
      }
    } catch (error) {
      handleRequestError(t("cashHubAllocations.errors.removeRecipient"), error);
    }
    setDeletingSingle(false);
  };

  const handleDeleteWallet = async (c: CashWalletClaim) => {
    if (!id) {
      return;
    }
    setDeletingSingle(true);
    try {
      await request(`/api/apps/${id}/cash-wallets/${c.wallet_app_id}`, {
        method: "DELETE",
      });
      toast(t("cashHubAllocations.walletRemovedToast"));
      setConfirmDeleteWallet(null);
      if (claims.length === 1 && page > 1) {
        setPage(page - 1);
      } else {
        await loadClaims();
      }
    } catch (error) {
      handleRequestError(t("cashHubAllocations.errors.removeWallet"), error);
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
            `/api/apps/${id}/cash-wallets/${c.wallet_app_id}/claims/${c.id}`,
            {
              method: "DELETE",
            }
          )
        )
      );
      toast(
        t("cashHubAllocations.recipientsRemovedToast", { count: rows.length })
      );
      setSelected(new Set());
      setConfirmBulkDeleteOpen(false);
      if (rows.length === claims.length && page > 1) {
        setPage(page - 1);
      } else {
        await loadClaims();
      }
    } catch (error) {
      handleRequestError(t("cashHubAllocations.errors.removeSelected"), error);
    }
    setRemovingSelected(false);
  };

  const totalAllocatedLoki = claims.reduce(
    (sum, c) => sum + c.amount_mloki / 1000,
    0
  );

  // One Cash wallet child (one NWC connection) can serve several
  // beneficiaries sharing a single funded pool — grouping the flat claims
  // page by wallet_app_id here shows one row per wallet (with its
  // beneficiaries nested inside) instead of repeating the same "Reveal NWC
  // connection" wallet-level action once per beneficiary. Order follows each
  // wallet's first appearance in `claims` (already newest-first from the
  // API); a wallet's beneficiaries can, in principle, straddle a page
  // boundary — grouping only ever affects rows already present on this page.
  const walletGroups = React.useMemo(() => {
    const order: number[] = [];
    const byWallet = new Map<number, CashWalletClaim[]>();
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
    value: CashAllocationStatus | "";
    label: string;
    count: number;
  }[] = [
    { value: "", label: t("cashHubAllocations.statusAll"), count: counts.all },
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
      label: t("cashHubAllocations.statusExpired"),
      count: counts.expired,
    },
  ];

  const recipientRowFields = (row: RecipientRow) => (
    <div key={row.key} className="grid gap-2 rounded-md border p-3">
      <div className="flex items-center justify-between gap-2">
        <Tabs
          value={row.identityType}
          onValueChange={(v) =>
            setRowIdentityType(
              row.key,
              v as "pubkey" | "connection_key" | "bearer"
            )
          }
        >
          <TabsList>
            <TabsTrigger value="pubkey">{t("identityType.pubkey")}</TabsTrigger>
            <TabsTrigger value="connection_key">
              {t("identityType.connectionKey")}
            </TabsTrigger>
            <TabsTrigger
              value="bearer"
              disabled={recipients.length > 1}
              title={
                recipients.length > 1
                  ? t("cashHubAllocations.bearerRequiresSoleRecipient")
                  : undefined
              }
            >
              {t("identityType.bearer")}
            </TabsTrigger>
          </TabsList>
        </Tabs>
        {recipients.length > 1 && (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            title={t("cashHubAllocations.removeRecipient")}
            aria-label={t("cashHubAllocations.removeRecipient")}
            className="text-muted-foreground hover:text-destructive"
            onClick={() => removeRow(row.key)}
          >
            <Trash2Icon className="size-4" />
          </Button>
        )}
      </div>
      <p className="text-sm text-muted-foreground">
        {row.identityType === "pubkey"
          ? t("cashHubAllocations.pubkeyModeHelper")
          : row.identityType === "connection_key"
            ? t("cashHubAllocations.connectionKeyModeHelper")
            : t("cashHubAllocations.bearerHelper")}
      </p>
      {row.identityType === "pubkey" ? (
        <NostrPubkeyInput
          id={`identityValue-${row.key}`}
          value={row.pubkeyValue}
          onChange={(v) => updateRow(row.key, { pubkeyValue: v })}
          onResolved={(v) => updateRow(row.key, { resolvedPubkeyHex: v })}
          label={t("cashHubAllocations.pubkeyLabel")}
          helperText={t("cashHubAllocations.pubkeyHelper")}
        />
      ) : row.identityType === "connection_key" ? (
        <>
          <div className="grid gap-1.5">
            <Label htmlFor={`identityValue-${row.key}`}>
              {t("cashHubAllocations.connectionKeyLabel")}
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
              {t("cashHubAllocations.iaPubkeyLabel")}
            </Label>
            {identityAuthorities.length > 0 &&
            iaModeFor(row, true) === "existing" ? (
              <Select
                value={row.iaPubkeyValue || undefined}
                onValueChange={(v) => {
                  if (v === MANUAL_IA_SENTINEL) {
                    updateRow(row.key, {
                      iaMode: "manual",
                      iaPubkeyValue: "",
                    });
                  } else {
                    updateRow(row.key, {
                      iaMode: "existing",
                      iaPubkeyValue: v,
                    });
                  }
                }}
              >
                <SelectTrigger id={`iaPubkey-${row.key}`} className="w-full">
                  <SelectValue
                    placeholder={t("cashHubAllocations.selectIAPlaceholder")}
                  />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectLabel>
                      {t("cashHubAllocations.savedIAsLabel")}
                    </SelectLabel>
                    {identityAuthorities.map((ia) => (
                      <SelectItem key={ia.pubkey} value={ia.pubkey}>
                        {ia.name} ({shortenMiddle(ia.pubkey, 6, 4)})
                      </SelectItem>
                    ))}
                  </SelectGroup>
                  <SelectSeparator />
                  <SelectItem value={MANUAL_IA_SENTINEL}>
                    {t("cashHubAllocations.enterIAManually")}
                  </SelectItem>
                </SelectContent>
              </Select>
            ) : (
              <>
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
                {identityAuthorities.length > 0 && (
                  <button
                    type="button"
                    className="justify-self-start text-xs text-muted-foreground hover:text-foreground hover:underline"
                    onClick={() =>
                      updateRow(row.key, {
                        iaMode: "existing",
                        iaPubkeyValue: "",
                      })
                    }
                  >
                    {t("cashHubAllocations.chooseSavedIAInstead")}
                  </button>
                )}
              </>
            )}
            <p className="text-sm text-muted-foreground">
              {t("cashHubAllocations.iaPubkeyHelper")}
            </p>
          </div>
        </>
      ) : // Bearer: no identity to collect — the wallet mints its own secret,
      // shown once right after creation (NIP-CASH §Bearer Slices). The
      // helper text above already covers this mode; nothing more to show.
      null}
      {isDuplicateRow(row) && (
        <p className="text-sm text-destructive">
          {t("cashHubAllocations.duplicateIdentity")}
        </p>
      )}
      <div className="grid gap-1.5">
        <Label htmlFor={`amount-${row.key}`}>
          {t("cashHubAllocations.walletBudgetLabel")}
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
          ? t("cashHubAllocations.requestedSummaryWithMax", {
              total: totalRequestedLoki.toLocaleString(),
              max: maxAmountLoki.toLocaleString(),
            })
          : t("cashHubAllocations.requestedSummary", {
              total: totalRequestedLoki.toLocaleString(),
            })}
      </p>
      {hasDeadline && (
        <div className="grid gap-1.5">
          <div className="flex items-center justify-between">
            <Label>{t("cashHubAllocations.claimDeadlineLabel")}</Label>
            <XIcon
              className="size-4 cursor-pointer text-muted-foreground"
              onClick={() => setHasDeadline(false)}
            />
          </div>
          <DurationInput
            seconds={claimDeadlineSecs}
            onChange={setClaimDeadlineSecs}
            // 0 means the hub has no ceiling at all ("never") — pass
            // undefined rather than 0, since DurationInput treats a 0 max as
            // "nothing is allowed" instead of "no cap".
            max={cashMaxExpSecs || undefined}
            // allowNever: a 0 (never) request is never rejected regardless of
            // the hub's own ceiling — it's sent as expiry_secs: 0, which the
            // backend treats exactly like omitting it entirely, deferring to
            // the hub's own default (itself "never" if the hub has no
            // ceiling, or the hub's max otherwise) — never an error.
            allowNever
          />
          <p
            className={cn(
              "text-sm",
              deadlineExceedsMax ? "text-destructive" : "text-muted-foreground"
            )}
          >
            {deadlineExceedsMax
              ? t("cashHubAllocations.deadlineExceeds", {
                  max: formatDurationLabel(cashMaxExpSecs, t),
                })
              : formatDurationLabel(cashMaxExpSecs, t)
                ? t("cashHubAllocations.deadlineHelpWithMax", {
                    max: formatDurationLabel(cashMaxExpSecs, t),
                  })
                : t("cashHubAllocations.deadlineHelp")}
          </p>
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        {!hasBearerRow && (
          <Button
            type="button"
            variant="secondary"
            className="w-fit"
            onClick={addRow}
          >
            <PlusCircleIcon /> {t("cashHubAllocations.addRecipient")}
          </Button>
        )}
        {!hasDeadline && (
          <Button
            type="button"
            variant="secondary"
            className="w-fit"
            onClick={() => {
              setClaimDeadlineSecs(cashMaxExpSecs || claimDeadlineSecs);
              setHasDeadline(true);
            }}
          >
            <PlusCircleIcon /> {t("cashHubAllocations.setClaimDeadline")}
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
              handleStatusChange(v as CashAllocationStatus | "")
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
          {t("cashHubAllocations.allocatedSummary", {
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
              {t("cashHubAllocations.recipientColumn")}
            </span>
          </div>
          <div className="grid gap-2 p-1 min-w-0">
            {displayGroups.map((group) => {
              const isMulti = group.claims.length > 1;
              const totalCount = group.claims.length;
              const claimedCount = group.claims.filter((c) => c.claimed).length;
              const totalLoki = group.claims.reduce(
                (sum, c) => sum + c.amount_mloki / 1000,
                0
              );
              // Deadline/expiry and cash_token are both properties of the
              // wallet App itself, shared by every beneficiary in the group —
              // safe to read off the first claim.
              const deadline = group.claims[0].expires_at
                ? formatClaimDeadline(group.claims[0].expires_at)
                : undefined;
              const lokicashToken = group.claims[0].cash_token;
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
              const canRemoveSingle =
                !isMulti && removableIds.has(group.claims[0].id);

              return (
                <div
                  key={group.claims[0].id}
                  className="rounded-md border min-w-0"
                >
                  {/* One row shape for every token, whether it has one
                      recipient or several — see the "same UI regardless of
                      recipient count" redesign note above displayGroups.
                      Only what a pill/expand genuinely needs (selecting a
                      GROUP of claims at once, having something to expand)
                      branches on isMulti; everything else renders once. */}
                  <div
                    className="flex min-w-0 cursor-pointer items-start gap-2 p-2 transition-colors hover:bg-accent/50 sm:items-center sm:gap-3"
                    onClick={() => navigate(`/apps/${group.walletAppId}`)}
                  >
                    <Checkbox
                      checked={
                        isMulti
                          ? groupAllSelected
                            ? true
                            : groupSomeSelected
                              ? "indeterminate"
                              : false
                          : selected.has(group.claims[0].id)
                      }
                      onCheckedChange={
                        isMulti
                          ? toggleGroupSelected
                          : () => toggleOne(group.claims[0].id)
                      }
                      onClick={(e) => e.stopPropagation()}
                      disabled={
                        isMulti
                          ? groupRemovableIds.length === 0
                          : !canRemoveSingle
                      }
                      aria-label={
                        isMulti
                          ? t("cashHubAllocations.selectAllInWallet")
                          : t("cashHubAllocations.selectRecipient")
                      }
                      className="mt-1 shrink-0 sm:mt-0"
                    />

                    <div
                      className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary/10 text-primary"
                      title={t("cashHubAllocations.lokicashBadge")}
                    >
                      <CoinsIcon className="h-3.5 w-3.5" />
                    </div>

                    <div className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row sm:items-center">
                      <div className="min-w-0 flex-1">
                        {/* The token's own string leads — this is a
                            Lokicash first, a recipient list second. Click
                            to copy directly; no separate copy button, which
                            would just be the same action twice. */}
                        {lokicashToken ? (
                          <button
                            type="button"
                            className="block max-w-full truncate rounded font-mono text-sm font-medium hover:underline"
                            title={t("cashHubAllocations.copyLokicash")}
                            onClick={(e) => {
                              e.stopPropagation();
                              copyToClipboard(lokicashToken);
                            }}
                          >
                            {shortenMiddle(lokicashToken, 14, 6)}
                          </button>
                        ) : (
                          <span className="block truncate font-mono text-sm text-muted-foreground">
                            lokicash1…
                          </span>
                        )}
                        {/* Identity pills — a small icon (or, for a pubkey
                            with a set profile picture, the recipient's own
                            avatar) plus a short identifier, deliberately no
                            display name here (NIP-CASH: a recipient's real
                            identity is proof-gated detail, not something
                            this summary needs to resolve — a verified nip05
                            or npub is a stable identifier, not a mutable
                            display name). Expand (multi) or click through
                            (single) for full detail. Same pill shape for one
                            recipient or many — a single-recipient row just
                            renders exactly one. */}
                        <div className="mt-1 flex flex-wrap items-center gap-1">
                          {group.claims.slice(0, maxVisiblePills).map((c) => (
                            <Badge
                              key={c.id}
                              variant="outline"
                              className="max-w-[10rem] gap-1 px-1.5 py-0 font-mono text-[11px] font-normal text-muted-foreground"
                            >
                              {c.identity_type === "pubkey" ? (
                                <PillIdentityGlyph
                                  profile={profiles.get(c.identity_value)}
                                />
                              ) : c.identity_type === "bearer" ? (
                                <BanknoteIcon className="h-3 w-3 shrink-0" />
                              ) : (
                                <KeyRound className="h-3 w-3 shrink-0" />
                              )}
                              <span className="truncate">
                                {c.identity_type === "bearer"
                                  ? t("identityType.bearer")
                                  : c.identity_type === "pubkey"
                                    ? pillIdentityLabel(
                                        c.identity_value,
                                        profiles.get(c.identity_value),
                                        verifiedNip05.has(c.identity_value)
                                      )
                                    : shortenMiddle(c.identity_value, 6, 4)}
                              </span>
                            </Badge>
                          ))}
                          {group.claims.length > maxVisiblePills && (
                            <Badge
                              variant="outline"
                              className="px-1.5 py-0 text-[11px] font-normal text-muted-foreground"
                            >
                              +{group.claims.length - maxVisiblePills}
                            </Badge>
                          )}
                        </div>
                      </div>

                      <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 sm:flex-nowrap sm:justify-end sm:gap-3">
                        <div
                          className="text-end tabular-nums"
                          title={deadline?.title}
                        >
                          <div className="flex items-center justify-end gap-2">
                            <span className="text-sm font-medium">
                              {totalLoki.toLocaleString()} loki
                            </span>
                            {claimedCount === totalCount ? (
                              <Badge variant="positive">
                                {t(
                                  "cashHubAllocations.tokenStatusFullyRedeemed"
                                )}
                              </Badge>
                            ) : claimedCount === 0 ? (
                              <Badge variant="secondary">
                                {t("cashHubAllocations.tokenStatusUnredeemed")}
                              </Badge>
                            ) : (
                              <Badge variant="outline">
                                {t(
                                  "cashHubAllocations.tokenStatusPartiallyRedeemed",
                                  {
                                    claimed: claimedCount,
                                    count: totalCount,
                                  }
                                )}
                              </Badge>
                            )}
                          </div>
                          <div className="text-xs text-muted-foreground">
                            {deadline?.label ?? t("claimDeadline.none")}
                          </div>
                        </div>

                        {isMulti && (
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
                                ? t("cashHubAllocations.collapseRecipients")
                                : t("cashHubAllocations.expandRecipients")
                            }
                          >
                            {isExpanded ? (
                              <ChevronDownIcon className="size-4 text-muted-foreground" />
                            ) : (
                              <ChevronRightIcon className="size-4 text-muted-foreground" />
                            )}
                          </Button>
                        )}

                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("cashHubAllocations.revealConnection")}
                          aria-label={t("cashHubAllocations.revealConnection")}
                          disabled={revealingWalletId === group.walletAppId}
                          onClick={(e) => {
                            e.stopPropagation();
                            handleRevealConnection(group.walletAppId, {
                              amountLoki: totalLoki,
                              recipientCount: totalCount,
                              claimedCount,
                              expiresAtSecs: group.claims[0].expires_at,
                            });
                          }}
                        >
                          <QrCodeIcon className="size-4" />
                        </Button>

                        <Button
                          variant="ghost"
                          size="icon"
                          title={
                            isMulti
                              ? t("cashHubAllocations.removeWallet")
                              : t("common.remove")
                          }
                          aria-label={
                            isMulti
                              ? t("cashHubAllocations.removeWallet")
                              : t("common.remove")
                          }
                          className={cn(
                            "text-muted-foreground hover:text-destructive",
                            !isMulti && !canRemoveSingle && "invisible"
                          )}
                          disabled={!isMulti && !canRemoveSingle}
                          onClick={(e) => {
                            e.stopPropagation();
                            if (isMulti) {
                              setConfirmDeleteWallet(group.claims[0]);
                            } else {
                              setConfirmDeleteClaim(group.claims[0]);
                            }
                          }}
                        >
                          <Trash2Icon className="size-4" />
                        </Button>
                      </div>
                    </div>
                  </div>

                  {isMulti && isExpanded && (
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
                              aria-label={t(
                                "cashHubAllocations.selectRecipient"
                              )}
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
            ? t("cashHubAllocations.noneFiltered", {
                status:
                  statusTabs.find((tab) => tab.value === status)?.label ??
                  status,
              })
            : t("cashHubAllocations.noneOnPage")}
        </p>
      ) : (
        <div className="rounded-lg border p-4 max-w-lg">
          <h3 className="mb-4 flex items-center gap-2 font-medium">
            <PlusIcon className="size-4 text-muted-foreground" />
            {t("cashHubAllocations.addHeading")}
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
        limit={LIST_CASH_ALLOCATIONS_LIMIT}
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
            <DialogTitle>
              {t("cashHubAllocations.createDialogTitle")}
            </DialogTitle>
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
          lokicashToken={revealLokicashToken}
          bearerSecret={revealBearerSecret}
          walletSummary={revealSummary}
          mode={revealMode}
          primaryFormat="lokicash"
          onClose={() => {
            setRevealUri(undefined);
            setRevealApp(undefined);
            setRevealLokicashToken(undefined);
            setRevealBearerSecret(undefined);
            setRevealSummary(undefined);
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
              {t("cashHubAllocations.removeRecipientTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription asChild>
              <p>{t("cashHubAllocations.removeRecipientDescription")}</p>
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
              {t("cashHubAllocations.removeWalletTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription asChild>
              <p>{t("cashHubAllocations.removeWalletDescription")}</p>
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
              {t("cashHubAllocations.removeSelectedTitle", {
                count: selected.size,
              })}
            </AlertDialogTitle>
            <AlertDialogDescription asChild>
              <p>{t("cashHubAllocations.removeSelectedDescription")}</p>
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
