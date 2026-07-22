import React from "react";
import { Copy, KeyRound } from "lucide-react";
import { useTranslation } from "react-i18next";

import { ClaimStateBadge } from "src/components/circles/ClaimStateBadge";
import { NostrIdentityHeader } from "src/components/circles/NostrIdentityHeader";
import { Avatar, AvatarFallback } from "src/components/ui/avatar";
import { Badge } from "src/components/ui/badge";
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
import { App, JITWalletClaim, ListJITWalletClaimsResponse } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { formatClaimDeadline } from "src/utils/jitWallet";
import { shortenMiddle } from "src/utils/nostr";
import { request } from "src/utils/request";

// Surfaces the identity/identities behind a JIT/circle wallet child app, at
// the top of its detail page. circle_wallet always has exactly one member
// identity (requester_pubkey, set by create_circle_wallet), so that case
// stays a single NostrIdentityHeader. jit_wallet is handled separately by
// JITWalletRecipientsCard below — a shared jit_wallet can serve more than
// one beneficiary now, so it can't reuse this single-identity rendering.
export function ChildIdentityCard({ app }: { app: App }) {
  const { t } = useTranslation("circles");

  if (app.kind === "jit_wallet") {
    return <JITWalletRecipientsCard app={app} />;
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

// One beneficiary's full identity (name/NIP-05/npub, or a connection-key
// fallback) alongside what they're owed: amount, claimed state, and claim
// deadline. Used for both the single- and multi-beneficiary cases so a
// wallet with several recipients reads as a stack of profiles rather than a
// denser table of rows.
function BeneficiaryProfile({
  claim,
  bordered,
}: {
  claim: JITWalletClaim;
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

// JITWalletRecipientsCard fetches and shows a jit_wallet's own recipients.
// Unlike circle_wallet above, this can't rely on a single pubkey off
// app.metadata — the create flow never populates that for jit_wallet;
// recipients live in JITWalletClaim rows instead, keyed by wallet_app_id,
// since one jit_wallet (one NWC connection) can serve several beneficiaries
// sharing one funded pool.
function JITWalletRecipientsCard({ app }: { app: App }) {
  const { t } = useTranslation("circles");
  const [recipients, setRecipients] = React.useState<
    JITWalletClaim[] | undefined
  >(undefined);

  React.useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const data = await request<ListJITWalletClaimsResponse>(
          `/api/apps/${app.id}/jit-wallet-recipients`
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
          <CardTitle>{t("childIdentityCard.jitWallet")}</CardTitle>
          <div className="flex items-center gap-3">
            <Skeleton className="h-12 w-12 rounded-full" />
            <Skeleton className="h-5 w-40" />
          </div>
        </CardHeader>
      </Card>
    );
  }

  if (recipients.length === 0) {
    return null;
  }

  if (recipients.length === 1) {
    return (
      <Card>
        <CardHeader className="gap-3">
          <CardTitle>{t("childIdentityCard.jitWallet")}</CardTitle>
          <BeneficiaryProfile claim={recipients[0]} />
        </CardHeader>
      </Card>
    );
  }

  const claimedCount = recipients.filter((r) => r.claimed).length;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          {t("childIdentityCard.jitWallet")}
          <Badge variant="secondary" className="tabular-nums font-normal">
            {t("childIdentityCard.claimedCount", {
              claimed: claimedCount,
              total: recipients.length,
            })}
          </Badge>
        </CardTitle>
      </CardHeader>
      <CardContent className="grid gap-3">
        {recipients.map((r) => (
          <BeneficiaryProfile key={r.id} claim={r} bordered />
        ))}
      </CardContent>
    </Card>
  );
}
