import { HelpCircle } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import AppHeader from "src/components/AppHeader";
import { SubWalletInfoDialog } from "src/components/SubWalletInfoDialog";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { getWalletTypes } from "src/screens/subwallets/walletTypes";

export function NewSubwallet() {
  const navigate = useNavigate();
  const { t } = useTranslation("wallet");
  const walletTypes = getWalletTypes(t);

  return (
    <div className="grid gap-5">
      <AppHeader
        title={t("subwallets.typeChooser.title")}
        description={t("subwallets.typeChooser.description")}
        contentRight={
          <SubWalletInfoDialog
            trigger={
              <Button variant="outline" size="icon">
                <HelpCircle className="size-4" />
              </Button>
            }
          />
        }
      />
      <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
        {walletTypes.map(({ to, icon: Icon, title, description }) => (
          <Card
            key={to}
            className="flex flex-col cursor-pointer transition-colors hover:bg-accent hover:text-accent-foreground hover:border-primary/50"
            onClick={() => navigate(to)}
          >
            <CardHeader>
              <Icon className="size-6 text-muted-foreground" />
              <CardTitle className="text-lg mt-2">{title}</CardTitle>
              <CardDescription>{description}</CardDescription>
            </CardHeader>
          </Card>
        ))}
      </div>
    </div>
  );
}
