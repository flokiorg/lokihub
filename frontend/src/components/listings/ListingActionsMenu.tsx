import { CopyIcon, EllipsisIcon, PencilIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { toast } from "sonner";
import { Button } from "src/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "src/components/ui/dropdown-menu";
import {
  type Listing,
  type ListingAction,
  listingAction,
} from "src/hooks/useListings";
import { cn } from "src/lib/utils";
import { handleRequestError } from "src/utils/handleRequestError";

function copy(text: string, done: string) {
  navigator.clipboard.writeText(text).then(() => toast(done));
}

// One glanceable word for a listing's state. Health detail (relay counts,
// retrying, operator) is carried by ListingHealth beside it.
export function StatusPill({ listing }: { listing: Listing }) {
  const { t } = useTranslation("listings");
  const base = "flex-none rounded-full px-2.5 py-1 text-xs font-medium";
  if (listing.phase === "closing") {
    return (
      <span className={cn(base, "bg-muted text-muted-foreground")}>
        {t("status.closing", "Closing")}
      </span>
    );
  }
  if (listing.status === "paused") {
    return (
      <span
        className={cn(
          base,
          "border border-warning-foreground/30 bg-warning-foreground/10 text-warning-foreground"
        )}
      >
        {t("status.paused")}
      </span>
    );
  }
  return (
    <span
      className={cn(
        base,
        "border border-positive-foreground/30 bg-positive-foreground/10 text-positive-foreground"
      )}
    >
      {t("status.listed")}
    </span>
  );
}

// The "…" actions menu shared by the listings list and the listing detail
// page, so edit / pause / resume / copy / check / unlist stay defined once.
// `onChanged` refreshes the caller's data after a mutating action. When
// `includeEdit` is false the detail page can show its own prominent Edit
// button instead of duplicating it here.
export function ListingActionsMenu({
  listing: l,
  onChanged,
  includeEdit = true,
}: {
  listing: Listing;
  onChanged: () => void | Promise<unknown>;
  includeEdit?: boolean;
}) {
  const { t } = useTranslation("listings");
  const navigate = useNavigate();

  const act = async (action: ListingAction) => {
    try {
      await listingAction(l.id, action);
      await onChanged();
    } catch (error) {
      handleRequestError(t("errors.action"), error);
    }
  };

  return (
    <DropdownMenu modal={false}>
      <Button variant="ghost" size="icon" asChild>
        <DropdownMenuTrigger aria-label={l.title || t("card.untitled")}>
          <EllipsisIcon className="size-4" />
        </DropdownMenuTrigger>
      </Button>
      <DropdownMenuContent align="end">
        {/* Edit / pause / resume / check / unlist are only valid while
            actually listed — a 'closing' listing is tearing down, so those
            are hidden. */}
        {includeEdit && l.phase === "listed" && (
          <DropdownMenuItem asChild>
            <div
              className="flex items-center gap-2"
              onClick={() => navigate(`/listings/edit/${l.id}`)}
            >
              <PencilIcon className="size-4" /> {t("actions.edit")}
            </div>
          </DropdownMenuItem>
        )}
        {l.phase === "listed" &&
          (l.status === "paused" ? (
            <DropdownMenuItem asChild>
              <div onClick={() => act("resume")}>{t("actions.resume")}</div>
            </DropdownMenuItem>
          ) : (
            <DropdownMenuItem asChild>
              <div onClick={() => act("pause")}>{t("actions.pause")}</div>
            </DropdownMenuItem>
          ))}
        {l.address && (
          <DropdownMenuItem asChild>
            <div
              className="flex items-center gap-2"
              onClick={() => copy(JSON.stringify(l.aTag), t("address.copied"))}
            >
              <CopyIcon className="size-4" /> {t("address.copyTag")}
            </div>
          </DropdownMenuItem>
        )}
        {l.operatorPubkey && (
          <DropdownMenuItem asChild>
            <div
              className="flex items-center gap-2"
              onClick={() => copy(l.operatorCommand, t("address.copied"))}
            >
              <CopyIcon className="size-4" /> {t("address.copyCommand")}
            </div>
          </DropdownMenuItem>
        )}
        {l.operatorPubkey && l.phase === "listed" && (
          <DropdownMenuItem asChild>
            <div onClick={() => act("check-operator")}>
              {t("actions.checkOperator")}
            </div>
          </DropdownMenuItem>
        )}
        {l.type === "circle_wallet" && l.connAppId > 0 && (
          <DropdownMenuItem asChild>
            <div onClick={() => act("revoke-connection")}>
              {t("actions.revokeConnection")}
            </div>
          </DropdownMenuItem>
        )}
        {l.phase === "listed" && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" asChild>
              <div onClick={() => act("unlist")}>{t("actions.unlist")}</div>
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
