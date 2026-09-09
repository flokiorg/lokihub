import { useTranslation } from "react-i18next";
import { Badge } from "src/components/ui/badge";
import { CashWalletClaim } from "src/types";

export function ClaimStateBadge({ claim }: { claim: CashWalletClaim }) {
  const { t } = useTranslation("circles");
  // spun_off_to_wallet_app_id is set for both a split and a consolidate — in
  // either case the recipient never actually redeemed via Lightning, their
  // value just moved into a brand-new wallet, so "Redeemed" would be
  // misleading here even though claimed is also true.
  if (claim.spun_off_to_wallet_app_id) {
    return (
      <Badge variant="outline">{t("claimBadge.movedToNewWallet")}</Badge>
    );
  }
  if (claim.claimed) {
    return <Badge variant="positive">{t("claimBadge.claimed")}</Badge>;
  }
  return <Badge variant="secondary">{t("claimBadge.unclaimed")}</Badge>;
}
