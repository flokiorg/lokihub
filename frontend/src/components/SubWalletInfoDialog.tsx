import { useTranslation } from "react-i18next";
import {
    AlertDialog,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
    AlertDialogTrigger,
} from "src/components/ui/alert-dialog";

type SubWalletInfoDialogProps = {
  trigger: React.ReactNode;
};

export function SubWalletInfoDialog({ trigger }: SubWalletInfoDialogProps) {
  const { t } = useTranslation("wallet");

  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        {trigger}
      </AlertDialogTrigger>
      <AlertDialogContent className="max-w-md">
        <AlertDialogHeader>
          <AlertDialogTitle>{t("subwallets.about.title")}</AlertDialogTitle>
          <div className="flex flex-col gap-4 text-muted-foreground text-sm">
            <p>{t("subwallets.about.desc1")}</p>
            <p>{t("subwallets.about.desc2")}</p>
          </div>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("subwallets.about.close")}</AlertDialogCancel>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
