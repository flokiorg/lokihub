import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { lspDisabledReason, shortNodeId } from "src/components/lsps/catalogue";
import { SelectItem } from "src/components/ui/select";
import type { LSP } from "src/types";

// TrustedLSPItems lists the trusted LSPs in an LSP dropdown (WP-26): one that
// is paused, closed, no longer announced or unreachable is shown disabled with
// the reason.
export function TrustedLSPItems({ lsps }: { lsps: LSP[] }) {
  const { t } = useTranslation("setup");
  return (
    <>
      {lsps.map((lsp) => {
        const reason = lspDisabledReason(lsp);
        const value = lsp.pubkey || lsp.nodeKey;
        return (
          <SelectItem key={value} value={value} disabled={!!reason}>
            {lsp.name || shortNodeId(lsp.nodeKey || lsp.pubkey)}
            {reason && ` (${t(`services.lsp.reason.${reason}`)})`}
          </SelectItem>
        );
      })}
    </>
  );
}

// NoTrustedLSP is shown where an LSP is needed and none is trusted.
export function NoTrustedLSP() {
  const { t } = useTranslation("setup");
  return (
    <p className="text-sm text-muted-foreground" data-testid="no-trusted-lsp">
      <Link to="/settings/services" className="underline">
        {t("services.lsp.noTrusted")}
      </Link>
    </p>
  );
}
