import { useTranslation } from "react-i18next";
import { Badge } from "src/components/ui/badge";
import { JITWalletClaim } from "src/types";

export function ClaimStateBadge({ claim }: { claim: JITWalletClaim }) {
  const { t } = useTranslation("circles");
  if (claim.claimed) {
    return <Badge variant="positive">{t("claimBadge.claimed")}</Badge>;
  }
  return <Badge variant="secondary">{t("claimBadge.unclaimed")}</Badge>;
}
