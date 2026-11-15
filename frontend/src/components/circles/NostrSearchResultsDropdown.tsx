import { useTranslation } from "react-i18next";
import { NostrProfileRow } from "src/components/circles/NostrProfileRow";
import { NostrProfileSearchResult } from "src/hooks/useNostrProfileSearch";
import { cn } from "src/lib/utils";

// Shared dropdown for the two "type an identity, or search a name" fields
// (owner identity, member picker manual-add). `position="above"` is for the
// manual-add box, which sits at the bottom of its panel — opening downward
// there would run off the panel (or get clipped), so it opens upward
// instead.
export function NostrSearchResultsDropdown({
  results,
  isSearching,
  position = "below",
  onPick,
}: {
  results: NostrProfileSearchResult[];
  isSearching: boolean;
  position?: "above" | "below";
  onPick: (pubkey: string) => void;
}) {
  const { t } = useTranslation("circles");
  return (
    <div
      className={cn(
        "absolute z-50 w-full rounded-lg border bg-popover text-popover-foreground shadow-md",
        position === "above" ? "bottom-full mb-1" : "top-full mt-1"
      )}
    >
      {results.length === 0 ? (
        <p className="p-3 text-sm text-muted-foreground">
          {isSearching
            ? t("searchDropdown.searching")
            : t("searchDropdown.noMatches")}
        </p>
      ) : (
        <ul className="max-h-64 overflow-y-auto p-1">
          {results.map(({ pubkey, profile }) => (
            <li key={pubkey}>
              <button
                type="button"
                onMouseDown={(e) => {
                  e.preventDefault();
                  onPick(pubkey);
                }}
                className="flex w-full items-center gap-3 rounded-sm px-2 py-1.5 text-start hover:bg-accent hover:text-accent-foreground"
              >
                <NostrProfileRow
                  pubkey={pubkey}
                  profile={profile}
                  avatarClassName="h-8 w-8"
                />
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
