import { Check, Loader2, X } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";

import { NostrProfileRow } from "src/components/circles/NostrProfileRow";
import {
  Command,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
} from "src/components/ui/command";
import { NostrAvatar } from "src/components/NostrAvatar";
import { useNip05Verification } from "src/hooks/useNip05Verification";
import { useNostrIdentityLookup } from "src/hooks/useNostrIdentityLookup";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { useNostrProfiles, NostrProfile } from "src/hooks/useNostrProfiles";
import { cn } from "src/lib/utils";
import { primaryProfileLabel } from "src/utils/nostrProfileLabel";

// MemberPicker searches Nostr globally (direct npub/hex/nprofile/NIP-05
// resolution, or a NIP-50 relay search for free text — via
// useNostrIdentityLookup, the same hook NostrPubkeyInput uses) and lets the
// owner hand-pick specific people as authorized circle members, rendered as
// removable chips. It's deliberately not scoped to the owner's following
// list at all — that's the "Anyone I follow" mode's job (see
// FollowingPreview), which is fully automatic and has no picker of its own.
export function MemberPicker({
  ownerPubkeyHex,
  selected,
  onChange,
  excludePubkeys = [],
}: {
  ownerPubkeyHex: string | undefined;
  selected: string[];
  onChange: (pubkeys: string[]) => void;
  // Pubkeys to hide from search results entirely — e.g. members already on
  // an existing allowlist, so the owner can't pick someone who's already in.
  excludePubkeys?: string[];
}) {
  const { t } = useTranslation("circles");
  const [query, setQuery] = React.useState("");
  const [isExpanded, setExpanded] = React.useState(false);

  const { profiles: selectedProfiles } = useNostrProfiles(selected);

  const {
    hex,
    isResolving,
    isSearchCandidate,
    results,
    isSearching,
  } = useNostrIdentityLookup(query);
  const { profile: directProfile } = useNostrProfile(hex);

  const searchProfiles = React.useMemo(
    () => new Map<string, NostrProfile>(results.map((r) => [r.pubkey, r.profile])),
    [results]
  );
  const { verified: verifiedNip05Pubkeys, pending: pendingNip05Pubkeys } =
    useNip05Verification(searchProfiles);

  const disabled = !ownerPubkeyHex;

  const toggle = (pubkey: string) => {
    onChange(
      selected.includes(pubkey)
        ? selected.filter((p) => p !== pubkey)
        : [...selected, pubkey]
    );
  };

  const remove = (pubkey: string) =>
    onChange(selected.filter((p) => p !== pubkey));

  const showDirectMatch =
    !!hex && hex !== ownerPubkeyHex && !excludePubkeys.includes(hex);
  const isAlreadyMemberMatch =
    !!hex && hex !== ownerPubkeyHex && excludePubkeys.includes(hex);
  // Deliberately keep already-excluded matches in the list (greyed out,
  // unselectable) instead of dropping them. The relay's NIP-50 `search`
  // only ever returns its own top N results for a query — if every one of
  // those already got added, filtering them out entirely leaves "No
  // matches on Nostr" for a name that plainly does match, which reads as
  // the search being broken rather than as "you already added everyone it
  // found."
  const visibleResults = results.filter((r) => r.pubkey !== ownerPubkeyHex);

  return (
    <div className="grid gap-3">
      {selected.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("memberPicker.noneYet")}</p>
      ) : (
        <div className="flex flex-wrap gap-2">
          {selected.map((pubkey) => (
            <div
              key={pubkey}
              className="flex items-center gap-1.5 rounded-full border bg-muted/50 py-1 ps-1 pe-2"
            >
              <NostrAvatar
                pubkey={pubkey}
                profile={selectedProfiles.get(pubkey)}
                className="h-5 w-5"
              />
              <span className="text-sm max-w-40 truncate">
                {primaryProfileLabel(pubkey, selectedProfiles.get(pubkey))}
              </span>
              <button
                type="button"
                onClick={() => remove(pubkey)}
                className="text-muted-foreground hover:text-foreground"
                aria-label={t("memberPicker.removeMember")}
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          ))}
        </div>
      )}

      {disabled ? (
        <p className="text-sm text-muted-foreground">
          {t("memberPicker.enterIdentity")}
        </p>
      ) : (
        <Command
          className="rounded-lg border"
          shouldFilter={false}
          onFocus={() => setExpanded(true)}
          onBlur={(e) => {
            if (e.currentTarget.contains(e.relatedTarget as Node | null)) {
              return;
            }
            // Deferred rather than immediate: this fires on mousedown, before
            // a click on an element outside this component (e.g. a dialog's
            // "Add" button) has registered. Collapsing synchronously here
            // reflows the list out from under the pointer before that click's
            // mouseup lands, so the click landed on whatever's now under the
            // cursor instead of the button — the button visually "ate" the
            // click that just closed the search instead of acting on it.
            // Re-checking focus after the click has had a chance to land
            // avoids collapsing this out from under a still-pending click.
            const container = e.currentTarget;
            window.setTimeout(() => {
              if (!container.contains(document.activeElement)) {
                setExpanded(false);
              }
            }, 150);
          }}
        >
          <CommandInput
            value={query}
            onValueChange={setQuery}
            placeholder={t("memberPicker.searchPlaceholder")}
          />
          {isExpanded && (
            <CommandList>
              {isAlreadyMemberMatch ? (
                <CommandEmpty>{t("memberPicker.alreadyMember")}</CommandEmpty>
              ) : showDirectMatch ? (
                <CommandItem
                  value={`direct-${hex}`}
                  onSelect={() => {
                    toggle(hex!);
                    setQuery("");
                    setExpanded(false);
                  }}
                  className={cn(
                    "flex items-center gap-3 border-s-2 py-2 px-3 cursor-pointer",
                    selected.includes(hex!)
                      ? "border-s-primary"
                      : "border-s-transparent"
                  )}
                >
                  <NostrProfileRow
                    pubkey={hex!}
                    profile={directProfile}
                    showCopy={false}
                  />
                  {selected.includes(hex!) && (
                    <Check className="h-4 w-4 shrink-0 text-primary" />
                  )}
                </CommandItem>
              ) : isSearchCandidate ? (
                visibleResults.length === 0 ? (
                  <CommandEmpty>
                    {isSearching ? (
                      <span className="flex items-center justify-center gap-2">
                        <Loader2 className="h-3.5 w-3.5 animate-spin" />
                        {t("memberPicker.searching")}
                      </span>
                    ) : (
                      t("memberPicker.noMatchesNostr")
                    )}
                  </CommandEmpty>
                ) : (
                  <>
                    {visibleResults.map(({ pubkey, profile }) => {
                      const isAlreadyMember = excludePubkeys.includes(pubkey);
                      const isPinned = selected.includes(pubkey);
                      const isNip05Verified = verifiedNip05Pubkeys.has(pubkey);
                      const isNip05Pending = pendingNip05Pubkeys.has(pubkey);
                      return (
                        <CommandItem
                          key={pubkey}
                          value={pubkey}
                          disabled={isAlreadyMember}
                          onSelect={() => {
                            if (isAlreadyMember) {return;}
                            toggle(pubkey);
                            if (visibleResults.length === 1) {
                              setQuery("");
                              setExpanded(false);
                            }
                          }}
                          className={cn(
                            "flex items-center gap-3 border-s-2 py-2 px-3",
                            isAlreadyMember
                              ? "cursor-default"
                              : "cursor-pointer",
                            isPinned ? "border-s-primary" : "border-s-transparent"
                          )}
                        >
                          <NostrProfileRow
                            pubkey={pubkey}
                            profile={profile}
                            isVerified={isNip05Verified}
                            isVerifying={isNip05Pending}
                            showCopy={false}
                          />
                          {isAlreadyMember ? (
                            <span className="shrink-0 text-xs text-muted-foreground">
                              {t("memberPicker.alreadyMemberBadge")}
                            </span>
                          ) : (
                            isPinned && (
                              <Check className="h-4 w-4 shrink-0 text-primary" />
                            )
                          )}
                        </CommandItem>
                      );
                    })}
                    {isSearching && (
                      <div className="flex items-center gap-2 px-3 py-2 text-sm text-muted-foreground">
                        <Loader2 className="h-3.5 w-3.5 animate-spin" />
                        {t("memberPicker.searching")}
                      </div>
                    )}
                  </>
                )
              ) : isResolving ? (
                <CommandEmpty>
                  <span className="flex items-center justify-center gap-2">
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                    {t("memberPicker.resolving")}
                  </span>
                </CommandEmpty>
              ) : (
                <CommandEmpty>
                  {t("memberPicker.searchHint")}
                </CommandEmpty>
              )}
            </CommandList>
          )}
        </Command>
      )}
    </div>
  );
}
