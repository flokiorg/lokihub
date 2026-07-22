import { AlertCircle, Loader2 } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";

import { NostrProfileRow } from "src/components/circles/NostrProfileRow";
import { NostrSearchResultsDropdown } from "src/components/circles/NostrSearchResultsDropdown";
import { Button } from "src/components/ui/button";
import { InputWithAdornment } from "src/components/ui/custom/input-with-adornment";
import { Label } from "src/components/ui/label";
import { useNostrIdentityLookup } from "src/hooks/useNostrIdentityLookup";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { safeNpubEncode } from "src/utils/nostr";

// NostrPubkeyInput accepts hex, npub1..., nprofile1..., a NIP-05 address
// (name@domain.com), or free text — surfacing live valid/resolving/invalid
// feedback via an adornment icon. Free text runs a NIP-50 relay search
// (useNostrIdentityLookup) and shows matches in a dropdown below the input;
// picking one fills the field with that person's npub, which resolves
// synchronously like any pasted npub. The rest of the form gates on
// `onResolved`'s hex value rather than re-validating the raw text itself.
//
// Once an identity resolves, the raw npub text is swapped out for a
// profile card (avatar + name, via NostrProfileRow) with a "Change" action —
// there's only ever one value here, so showing the opaque npub string back
// to the user after they just picked a name off a list is a regression, not
// confirmation.
export function NostrPubkeyInput({
  id,
  value,
  onChange,
  onResolved,
  label,
  helperText: helperTextProp,
}: {
  id: string;
  value: string;
  onChange: (raw: string) => void;
  onResolved: (hex: string | undefined) => void;
  label?: string;
  helperText?: string;
}) {
  const { t } = useTranslation("circles");
  const resolvedLabel = label ?? t("pubkeyInput.defaultLabel");
  const defaultHelperText = helperTextProp ?? t("pubkeyInput.defaultHelper");
  const { hex, isResolving, isInvalid, isSearchCandidate, results, isSearching } =
    useNostrIdentityLookup(value);
  const [isFocused, setFocused] = React.useState(false);
  const { profile: resolvedProfile } = useNostrProfile(hex);
  const inputRef = React.useRef<HTMLInputElement>(null);
  const refocusAfterClear = React.useRef(false);

  React.useEffect(() => {
    onResolved(hex);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hex]);

  // "Change" clears the value so the input reappears, then refocuses it once
  // that re-render has actually happened — doing it synchronously in the
  // click handler would focus an input that's still one render away from
  // existing.
  React.useEffect(() => {
    if (!hex && refocusAfterClear.current) {
      inputRef.current?.focus();
      refocusAfterClear.current = false;
    }
  }, [hex]);

  // Shown any time free text is being typed, even once the current search
  // comes back empty — hiding the dropdown on zero results left users
  // watching "Searching…" flash and vanish with no explanation. Let
  // NostrSearchResultsDropdown itself render the "No matches" state instead.
  const showDropdown = isFocused && isSearchCandidate;
  // Free text isn't a malformed identity, it's a search query — don't show
  // the "doesn't look like a valid..." error for it, even though the
  // underlying resolver reports isInvalid (no hex resolved) the same way it
  // would for a typo'd npub.
  const showInvalid = isInvalid && !isSearchCandidate;

  const pick = (pubkey: string) => {
    onChange(safeNpubEncode(pubkey) ?? pubkey);
    setFocused(false);
  };

  const changeIdentity = () => {
    refocusAfterClear.current = true;
    onChange("");
  };

  const helperText = showInvalid
    ? t("pubkeyInput.invalidHelper")
    : defaultHelperText;

  if (hex) {
    return (
      <div className="w-full grid gap-1.5">
        <Label>{resolvedLabel}</Label>
        <div className="flex items-center gap-3 rounded-lg border bg-muted/30 py-1.5 ps-2 pe-1.5">
          <NostrProfileRow pubkey={hex} profile={resolvedProfile} avatarClassName="h-9 w-9" />
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="shrink-0"
            onClick={changeIdentity}
          >
            {t("pubkeyInput.change")}
          </Button>
        </div>
        <p className="text-muted-foreground text-sm">{defaultHelperText}</p>
      </div>
    );
  }

  return (
    <div className="w-full grid gap-1.5">
      <Label htmlFor={id}>{resolvedLabel}</Label>
      <div className="relative">
        <InputWithAdornment
          ref={inputRef}
          id={id}
          type="text"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
          placeholder={t("pubkeyInput.placeholder")}
          autoComplete="off"
          endAdornment={
            isResolving || isSearching ? (
              <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
            ) : showInvalid ? (
              <AlertCircle className="h-4 w-4 text-destructive" />
            ) : null
          }
        />
        {showDropdown && (
          <NostrSearchResultsDropdown
            results={results}
            isSearching={isSearching}
            position="below"
            onPick={pick}
          />
        )}
      </div>
      <p className="text-muted-foreground text-sm">{helperText}</p>
    </div>
  );
}
