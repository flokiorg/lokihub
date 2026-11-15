import { useTranslation } from "react-i18next";
import type { OwnerLock } from "src/components/cash/locks";
import { Badge } from "src/components/ui/badge";

// OwnerLockBadge says why a slice or bill offers no remove or delete.
export function OwnerLockBadge({ lock }: { lock: OwnerLock }) {
  const { t } = useTranslation("circles");
  return (
    <Badge variant="warning" className="px-1.5 py-0 text-[11px] font-normal">
      {t(`cashLock.${lock}`)}
    </Badge>
  );
}
