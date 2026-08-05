import { PlusIcon, ShieldCheckIcon, Trash2 } from "lucide-react";
import React from "react";
import { NostrProfileRow } from "src/components/circles/NostrProfileRow";
import { NostrPubkeyInput } from "src/components/circles/NostrPubkeyInput";
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
import { buttonVariants } from "src/components/ui/buttonVariants";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Label } from "src/components/ui/label";
import { Textarea } from "src/components/ui/textarea";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { useNostrProfiles } from "src/hooks/useNostrProfiles";
import { cn } from "src/lib/utils";
import { IdentityAuthority } from "src/types";
import { primaryProfileLabel } from "src/utils/nostrProfileLabel";

// splitRelayUrls accepts either comma- or newline-separated relay URLs (users
// naturally reach for whichever separator matches how they copied the list).
function splitRelayUrls(raw: string): string[] {
  return raw
    .split(/[\n,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

interface IdentityAuthorityManagementCardProps {
  localAuthorities: IdentityAuthority[];
  setLocalAuthorities: (authorities: IdentityAuthority[]) => void;
  className?: string;
}

// Mirrors LSPManagementCard: operates purely on local state handed down by
// the parent screen. Additions/removals only mutate that local list — the
// actual API calls happen once, in Services.tsx's "Save Services" flow via
// saveIdentityAuthorityChanges, instead of firing a request per row edit.
export function IdentityAuthorityManagementCard({
  localAuthorities,
  setLocalAuthorities,
  className,
}: IdentityAuthorityManagementCardProps) {
  const { profiles } = useNostrProfiles(localAuthorities.map((a) => a.pubkey));

  const [isAdding, setIsAdding] = React.useState(false);
  const [pubkeyValue, setPubkeyValue] = React.useState("");
  const [resolvedPubkeyHex, setResolvedPubkeyHex] = React.useState<
    string | undefined
  >(undefined);
  const [relayUrlsValue, setRelayUrlsValue] = React.useState("");

  const { profile: resolvedProfile } = useNostrProfile(resolvedPubkeyHex);

  const [pendingRemove, setPendingRemove] =
    React.useState<IdentityAuthority | null>(null);

  const resetForm = () => {
    setPubkeyValue("");
    setResolvedPubkeyHex(undefined);
    setRelayUrlsValue("");
    setIsAdding(false);
  };

  const handleAdd = () => {
    if (!resolvedPubkeyHex) {
      return;
    }
    if (localAuthorities.some((a) => a.pubkey === resolvedPubkeyHex)) {
      resetForm();
      return;
    }

    const name = primaryProfileLabel(resolvedPubkeyHex, resolvedProfile);

    setLocalAuthorities([
      ...localAuthorities,
      {
        pubkey: resolvedPubkeyHex,
        name,
        relay_urls: splitRelayUrls(relayUrlsValue),
        created_at: Math.floor(Date.now() / 1000),
        // A brand-new local addition has nothing attesting to it yet — 0 is
        // both the safe default and the accurate value (mirrors the backend's
        // own AddIdentityAuthority response, which doesn't compute this
        // either; only ListIdentityAuthorities does).
        unredeemed_slice_count: 0,
      },
    ]);
    resetForm();
  };

  // Only mutates local state — the actual DELETE call happens later, when
  // the parent Settings screen's "Save Services" flow calls
  // saveIdentityAuthorityChanges. Called from IdentityAuthorityRemoveAlert's
  // confirm action, never directly from the trash button (see pendingRemove).
  const handleRemove = (pubkey: string) => {
    setLocalAuthorities(localAuthorities.filter((a) => a.pubkey !== pubkey));
  };

  return (
    <>
      <Card className={className}>
        <CardHeader className="pb-3">
          <CardTitle className="text-lg flex items-center gap-2">
            <ShieldCheckIcon className="w-5 h-5 text-primary" />
            Identity Authorities
          </CardTitle>
          <CardDescription>
            Nostr identities you trust to attest connection_key ownership claims
            for Cash wallets. An untrusted IA is rejected at wallet creation,
            but trust is also checked live on every redeem and transfer — so
            removing one here immediately blocks every unredeemed slice it has
            ever attested for, not just future wallets.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {localAuthorities.length > 0 && (
            <div className="rounded-lg border">
              <div className="grid gap-1 p-1">
                {localAuthorities.map((a) => {
                  const profile = profiles.get(a.pubkey);
                  const unredeemedCount = a.unredeemed_slice_count;
                  return (
                    <div
                      key={a.pubkey}
                      className="group flex items-center gap-3 rounded-md p-2 transition-colors hover:bg-accent/50"
                    >
                      <div className="flex min-w-0 flex-1 items-center gap-3">
                        <NostrProfileRow
                          pubkey={a.pubkey}
                          profile={profile}
                          avatarClassName="h-9 w-9"
                        />
                      </div>
                      {unredeemedCount > 0 && (
                        <Badge
                          variant="secondary"
                          className="shrink-0"
                          title="Currently-unredeemed Cash Wallet slices this IA has attested for"
                        >
                          {unredeemedCount} unredeemed
                        </Badge>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-6 w-6 shrink-0 text-muted-foreground hover:text-destructive hover:bg-destructive/10 opacity-0 group-hover:opacity-100 transition-opacity"
                        onClick={() => setPendingRemove(a)}
                      >
                        <Trash2 className="w-3 h-3" />
                      </Button>
                    </div>
                  );
                })}
              </div>
            </div>
          )}

          {localAuthorities.length === 0 && !isAdding && (
            <p className="text-sm text-muted-foreground">
              No trusted Identity Authorities yet.
            </p>
          )}

          {/* Add New Identity Authority — same inline expanding-card pattern as
            LSPManagementCard's "Add Custom LSP", no modal. */}
          <div
            className={cn(
              "relative flex flex-col p-4 rounded-xl border transition-all duration-200",
              isAdding
                ? "bg-card shadow-sm border-primary ring-1 ring-primary"
                : "bg-transparent border-border hover:border-primary hover:shadow-sm cursor-pointer group"
            )}
            onClick={() => !isAdding && setIsAdding(true)}
          >
            {!isAdding ? (
              <div className="flex flex-col items-center justify-center py-4 text-muted-foreground group-hover:text-primary transition-colors">
                <PlusIcon className="w-5 h-5 mb-1" />
                <span className="font-medium text-sm">
                  Add Identity Authority
                </span>
              </div>
            ) : (
              <div
                className="space-y-3 animate-in fade-in zoom-in-95 duration-200"
                onClick={(e) => e.stopPropagation()}
              >
                <NostrPubkeyInput
                  id="ia-pubkey"
                  value={pubkeyValue}
                  onChange={setPubkeyValue}
                  onResolved={setResolvedPubkeyHex}
                  label="Identity Authority"
                  helperText="Search by name, NIP-05, or paste an npub/hex/nprofile"
                />
                <div className="space-y-1">
                  <Label htmlFor="ia-relays" className="text-xs">
                    Relay URLs
                  </Label>
                  <Textarea
                    id="ia-relays"
                    value={relayUrlsValue}
                    onChange={(e) => setRelayUrlsValue(e.target.value)}
                    placeholder="wss://relay.example.com"
                    rows={2}
                    className="text-xs bg-background"
                  />
                  <p className="text-[10px] text-muted-foreground">
                    Optional, comma- or newline-separated. Stored for reference
                    only — attestation verification never fetches from relays.
                  </p>
                </div>
                <div className="flex items-center gap-2 pt-1">
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="flex-1 h-7 text-xs"
                    onClick={resetForm}
                  >
                    Cancel
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    className="flex-1 h-7 text-xs"
                    onClick={handleAdd}
                    disabled={!resolvedPubkeyHex}
                  >
                    Add
                  </Button>
                </div>
              </div>
            )}
          </div>
        </CardContent>
      </Card>

      <IdentityAuthorityRemoveAlert
        authority={pendingRemove}
        onClose={() => setPendingRemove(null)}
        onConfirm={handleRemove}
      />
    </>
  );
}

// IdentityAuthorityRemoveAlert mirrors ManageIdentitiesDialog.tsx's
// DeleteIdentityAlert pattern, with one deliberate difference: it never
// disables/blocks the action based on unredeemed_slice_count. Unlike a
// CircleIdentity (where deletion is blocked while in-use), revoking a
// compromised or untrusted IA needs to stay possible even when it currently
// has live exposure — that's the whole point of revoking it. The dialog's
// job is a strong, accurate warning, not a gate.
function IdentityAuthorityRemoveAlert({
  authority,
  onClose,
  onConfirm,
}: {
  authority: IdentityAuthority | null;
  onClose: () => void;
  onConfirm: (pubkey: string) => void;
}) {
  const unredeemedCount = authority?.unredeemed_slice_count ?? 0;

  return (
    <AlertDialog open={!!authority} onOpenChange={(o) => !o && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            Remove {authority?.name ?? "this Identity Authority"}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            {unredeemedCount > 0 ? (
              <>
                This will block {unredeemedCount} currently-unredeemed Cash
                Wallet slice{unredeemedCount === 1 ? "" : "s"} it has attested
                for the moment you save — including funds already sent to real
                recipients, which will become unreachable. This can't be undone
                by re-adding the same Identity Authority.
              </>
            ) : (
              <>
                This Identity Authority has no currently-unredeemed slices
                attesting to it right now. Removing it will stop it from being
                trusted for any future Cash wallet.
              </>
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onClose}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            className={buttonVariants({ variant: "destructive" })}
            onClick={() => {
              if (authority) {
                onConfirm(authority.pubkey);
              }
              onClose();
            }}
          >
            Remove
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
