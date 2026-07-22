import {
    CirclePlusIcon,
    HandCoins,
    HelpCircle,
    TriangleAlert,
    Wallet2,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import AppHeader from "src/components/AppHeader";

import SubWalletDark from "src/assets/illustrations/sub-wallet-dark.svg?react";
import SubWalletLight from "src/assets/illustrations/sub-wallet-light.svg?react";
import ResponsiveLinkButton from "src/components/ResponsiveLinkButton";
import { SubWalletInfoDialog } from "src/components/SubWalletInfoDialog";
import { Button } from "src/components/ui/button";
import { getWalletTypes } from "src/screens/subwallets/walletTypes";

export function SubwalletIntro() {
  const { t } = useTranslation("wallet");
  const walletTypes = getWalletTypes(t);

  return (
    <div className="grid gap-4">
      <AppHeader
        title={t("subwallets.title")}
        description={t("subwallets.intro.description")}
        contentRight={
          <>
            <SubWalletInfoDialog
              trigger={
                <Button variant="outline" size="icon">
                  <HelpCircle className="size-4" />
                </Button>
              }
            />
            <ResponsiveLinkButton
              to="/sub-wallets/new"
              icon={CirclePlusIcon}
              text={t("subwallets.new")}
            />
          </>
        }
      />
      <div>
        <div className="flex flex-col gap-6 max-w-(--breakpoint-md) pb-6">
          <div className="mb-2 flex justify-center sm:justify-start">
            <SubWalletDark className="w-72 hidden dark:block" />
            <SubWalletLight className="w-72 dark:hidden" />
          </div>
          <div>
            <div className="flex flex-row gap-3">
              <Wallet2 className="size-6" />
              <div className="font-medium">
                {t("subwallets.intro.separateWalletsTitle")}
              </div>
            </div>
            <div className="ms-9 text-muted-foreground text-sm">
              {t("subwallets.intro.separateWalletsDesc")}
            </div>
          </div>
          <div>
            <div className="flex flex-row gap-3">
              <HandCoins className="size-6" />
              <div className="font-medium">
                {t("subwallets.intro.dependOnBalanceTitle")}
              </div>
            </div>
            <div className="ms-9 text-muted-foreground text-sm">
              {t("subwallets.intro.dependOnBalanceDesc")}
            </div>
          </div>
          <div>
            <div className="flex flex-row gap-3">
              <TriangleAlert className="size-6" />
              <div className="font-medium">
                {t("subwallets.intro.waryOfSpendingTitle")}
              </div>
            </div>
            <div className="ms-9 text-muted-foreground text-sm">
              {t("subwallets.intro.waryOfSpendingDesc")}
            </div>
          </div>
          <div>
            <div className="font-medium mb-3">
              {t("subwallets.intro.walletTypesTitle")}
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              {walletTypes.map(({ to, icon: Icon, title, description }) => (
                <Link
                  key={to}
                  to={to}
                  className="flex flex-col gap-1 rounded-lg border bg-card p-4 shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground hover:border-primary/50"
                >
                  <Icon className="size-5 text-muted-foreground" />
                  <div className="font-medium text-sm">{title}</div>
                  <div className="text-muted-foreground text-xs">
                    {description}
                  </div>
                </Link>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
