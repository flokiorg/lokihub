import { useTranslation } from "react-i18next";
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
import {
  acceptedTerms,
  changedFields,
  fieldName,
  formatValue,
} from "src/components/lsps/terms";
import { cn } from "src/lib/utils";
import type { LSPAcceptedTerm, LSPTerms } from "src/types";

type Props = {
  // terms open the dialog; null closes it.
  terms: LSPTerms | null;
  onConfirm: (accepted: LSPAcceptedTerm[]) => void;
  onCancel: () => void;
};

// TermsChangedDialog shows the LSP's live terms next to the announced ones
// when they are worse, and asks again before buying (NIP-LSP live check).
export function TermsChangedDialog({ terms, onConfirm, onCancel }: Props) {
  const { t } = useTranslation("channels");
  return (
    <AlertDialog
      open={!!terms}
      onOpenChange={(open) => {
        if (!open) {
          onCancel();
        }
      }}
    >
      {terms && (
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("lspTerms.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("lspTerms.description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <table className="w-full text-sm">
            <thead>
              <tr className="text-muted-foreground text-start">
                <th className="text-start font-normal">
                  {t("lspTerms.value")}
                </th>
                <th className="text-start font-normal">
                  {t("lspTerms.announced")}
                </th>
                <th className="text-start font-normal">{t("lspTerms.live")}</th>
              </tr>
            </thead>
            <tbody>
              {changedFields(terms).map((f) => (
                <tr key={f.field}>
                  <td className="font-mono break-all pe-2">
                    {fieldName(f.field)}
                  </td>
                  <td className="pe-2">{formatValue(f.field, f.announced)}</td>
                  <td
                    className={cn(
                      f.verdict === "worse"
                        ? "text-destructive font-medium"
                        : "text-muted-foreground"
                    )}
                  >
                    {formatValue(f.field, f.live)}{" "}
                    <span className="text-xs">
                      ({t(`lspTerms.verdict.${f.verdict}`)})
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={onCancel}>
              {t("lspTerms.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction onClick={() => onConfirm(acceptedTerms(terms))}>
              {t("lspTerms.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      )}
    </AlertDialog>
  );
}
