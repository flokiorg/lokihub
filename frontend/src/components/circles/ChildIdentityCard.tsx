import React from "react";
import { BanknoteIcon, Copy, CoinsIcon, KeyRound, QrCodeIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { ClaimStateBadge } from "src/components/circles/ClaimStateBadge";
import { NostrIdentityHeader } from "src/components/circles/NostrIdentityHeader";
import { RevealConnectionDialog } from "src/components/connections/RevealConnectionDialog";
import { Avatar, AvatarFallback } from "src/components/ui/avatar";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Skeleton } from "src/components/ui/skeleton";
import { useNostrProfiles } from "src/hooks/useNostrProfiles";
import { cn } from "src/lib/utils";
import { copyToClipboard } from "src/lib/clipboard";
import {
  App,
  CashWalletClaim,
  CashWalletConnectionResponse,
  ListCashWalletClaimsResponse,
} from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { formatClaimDeadline } from "src/utils/cashWallet";
import { shortenMiddle } from "src/utils/nostr";
import { request } from "src/utils/request";

// Surfaces the identity/identities behind a Cash/circle wallet child app, at
// the top of its detail page. circle_wallet always has exactly one member
// identity (requester_pubkey, set by create_circle_wallet), so that case
// stays a single NostrIdentityHeader. cash_wallet is handled separately by
// CashWalletRecipientsCard below — a shared cash_wallet can serve more than
// one beneficiary now, so it can't reuse this single-identity rendering.
export function ChildIdentityCard({ app }: { app: App }) {
  const { t } = useTranslation("circles");

  if (app.kind === "cash_wallet") {
    return <CashWalletRecipientsCard app={app} />;
  }

  const identityPubkey = app.metadata?.requester_pubkey;

  if (identityPubkey) {
    return (
      <Card>
        <CardHeader className="gap-3">
          <CardTitle>{t("childIdentityCard.circleWallet")}</CardTitle>
          <NostrIdentityHeader pubkey={identityPubkey} />
        </CardHeader>
      </Card>
    );
  }

  return null;
}

function ConnectionKeyAvatarRow({ identityValue }: { identityValue: string }) {
  return (
    <div className="flex items-center gap-3">
      <Avatar className="h-12 w-12 shrink-0">
        <AvatarFallback>
          <KeyRound className="h-5 w-5 text-muted-foreground" />
        </AvatarFallback>
      </Avatar>
      <button
        type="button"
        onClick={() => copyToClipboard(identityValue)}
        className="flex min-w-0 items-center gap-2 text-start font-mono text-sm text-muted-foreground hover:text-foreground"
      >
        <span className="truncate">{shortenMiddle(identityValue)}</span>
        <Copy className="h-3.5 w-3.5 shrink-0" />
      </button>
    </div>
  );
}

// A bearer slice has no identity at all — identity_value here is a one-way
// commitment to the minted secret (NIP-CASH §Bearer Slices), not something
// to display or let an admin copy as if it identified a recipient.
function BearerAvatarRow() {
  const { t } = useTranslation("circles");
  return (
    <div className="flex items-center gap-3">
      <Avatar className="h-12 w-12 shrink-0">
        <AvatarFallback>
          <BanknoteIcon className="h-5 w-5 text-muted-foreground" />
        </AvatarFallback>
      </Avatar>
      <span className="text-sm text-muted-foreground">
        {t("identityType.bearer")}
      </span>
    </div>
  );
}

// One beneficiary's full identity (name/NIP-05/npub, or a connection-key
// fallback) alongside what they're owed: amount, claimed state, and claim
// deadline. Used for both the single- and multi-beneficiary cases so a
// wallet with several recipients reads as a stack of profiles rather than a
// denser table of rows.
function BeneficiaryProfile({
  claim,
  bordered,
}: {
  claim: CashWalletClaim;
  bordered?: boolean;
}) {
  const { t } = useTranslation("circles");
  const deadline = claim.expires_at
    ? formatClaimDeadline(claim.expires_at)
    : undefined;

  return (
    <div
      className={cn(
        "flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between",
        bordered && "rounded-lg border p-3"
      )}
    >
      {claim.identity_type === "pubkey" ? (
        <NostrIdentityHeader pubkey={claim.identity_value} />
      ) : claim.identity_type === "bearer" ? (
        <BearerAvatarRow />
      ) : (
        <ConnectionKeyAvatarRow identityValue={claim.identity_value} />
      )}
      <div
        className="text-end tabular-nums sm:shrink-0"
        title={deadline?.title}
      >
        <div className="flex items-center gap-2 sm:justify-end">
          <span className="text-sm font-medium">
            {(claim.amount_mloki / 1000).toLocaleString()} loki
          </span>
          <ClaimStateBadge claim={claim} />
        </div>
        <div className="text-xs text-muted-foreground">
          {deadline?.label ?? t("claimDeadline.none")}
        </div>
      </div>
    </div>
  );
}

// CashWalletRecipientsCard fetches and shows a cash_wallet's own recipients.
// Unlike circle_wallet above, this can't rely on a single pubkey off
// app.metadata — the create flow never populates that for cash_wallet;
// recipients live in CashWalletClaim rows instead, keyed by wallet_app_id,
// since one cash_wallet (one NWC connection) can serve several beneficiaries
// sharing one funded pool.
function CashWalletRecipientsCard({ app }: { app: App }) {
  const { t } = useTranslation("circles");
  const [recipients, setRecipients] = React.useState<
    CashWalletClaim[] | undefined
  >(undefined);
  const [connection, setConnection] = React.useState<
    CashWalletConnectionResponse | undefined
  >(undefined);
  const [showReveal, setShowReveal] = React.useState(false);

  React.useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const data = await request<ListCashWalletClaimsResponse>(
          `/api/apps/${app.id}/cash-wallet-recipients`
        );
        if (!cancelled) {
          setRecipients(data?.claims ?? []);
        }
      } catch (error) {
        if (!cancelled) {
          handleRequestError(
            t("childIdentityCard.errors.loadRecipients"),
            error
          );
          setRecipients([]);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [app.id, t]);

  // The wallet's own connection is deterministically re-derivable at any
  // time (NIP-CASH §The Pairing Connection) and doesn't need to be kept
  // secret (NIP-CASH §The Lokicash Token) — fetched eagerly, same as the
  // recipient roster above, so the token is already on screen instead of
  // gated behind a separate reveal click.
  React.useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const data = await request<CashWalletConnectionResponse>(
          `/api/apps/${app.id}/cash-connection`
        );
        if (!cancelled && data) {
          setConnection(data);
        }
      } catch (error) {
        if (!cancelled) {
          handleRequestError(t("cashHubAllocations.errors.loadConnection"), error);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [app.id, t]);

  const pubkeyIdentities = React.useMemo(
    () =>
      (recipients ?? [])
        .filter((r) => r.identity_type === "pubkey")
        .map((r) => r.identity_value),
    [recipients]
  );
  // Primes the shared profile cache with one batched relay fetch, so the
  // NostrIdentityHeader each BeneficiaryProfile renders below resolves
  // instantly instead of opening a subscription per beneficiary.
  useNostrProfiles(pubkeyIdentities);

  if (recipients === undefined) {
    return (
      <Card>
        <CardHeader className="gap-3">
          <CardTitle>{t("childIdentityCard.cashWallet")}</CardTitle>
          <div className="flex items-center gap-2">
            <Skeleton className="h-7 w-7 shrink-0 rounded-full" />
            <Skeleton className="h-5 w-48" />
          </div>
        </CardHeader>
      </Card>
    );
  }

  if (recipients.length === 0) {
    return null;
  }

  const claimedCount = recipients.filter((r) => r.claimed).length;

  // Same "lead with the token" template the Cash Hub's own allocations table
  // uses (NIP-CASH §The Lokicash Token) — a single recipient just renders its
  // one full identity below the token instead of a scrollable list.
  return (
    <>
      <Card>
        <CardHeader className="gap-3">
          <CardTitle className="flex flex-wrap items-center gap-2">
            {t("childIdentityCard.cashWallet")}
            {recipients.length > 1 && (
              <Badge variant="secondary" className="tabular-nums font-normal">
                {t("childIdentityCard.claimedCount", {
                  claimed: claimedCount,
                  total: recipients.length,
                })}
              </Badge>
            )}
          </CardTitle>
          <div className="flex items-center gap-2">
            <div
              className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary/10 text-primary"
              title={t("cashHubAllocations.lokicashBadge")}
            >
              <CoinsIcon className="h-3.5 w-3.5" />
            </div>
            {connection ? (
              <button
                type="button"
                onClick={() => copyToClipboard(connection.cash_token)}
                title={t("cashHubAllocations.copyLokicash")}
                className="min-w-0 flex-1 truncate text-start font-mono text-sm font-medium hover:underline"
              >
                {shortenMiddle(connection.cash_token, 14, 6)}
              </button>
            ) : (
              <Skeleton className="h-5 w-48" />
            )}
            <Button
              variant="ghost"
              size="icon"
              className="shrink-0"
              title={t("cashHubAllocations.revealConnection")}
              aria-label={t("cashHubAllocations.revealConnection")}
              disabled={!connection}
              onClick={() => setShowReveal(true)}
            >
              <QrCodeIcon className="size-4" />
            </Button>
          </div>
          {recipients.length === 1 && (
            <BeneficiaryProfile claim={recipients[0]} />
          )}
        </CardHeader>
        {recipients.length > 1 && (
          <CardContent className="grid gap-3">
            {recipients.map((r) => (
              <BeneficiaryProfile key={r.id} claim={r} bordered />
            ))}
          </CardContent>
        )}
      </Card>
      {showReveal && connection && (
        <RevealConnectionDialog
          app={app}
          pairingUri={connection.pairing_uri}
          lokicashToken={connection.cash_token}
          walletSummary={{
            amountLoki: recipients.reduce(
              (sum, r) => sum + r.amount_mloki / 1000,
              0
            ),
            recipientCount: recipients.length,
            claimedCount,
            expiresAtSecs: recipients[0].expires_at,
          }}
          primaryFormat="lokicash"
          onClose={() => setShowReveal(false)}
        />
      )}
    </>
  );
}
