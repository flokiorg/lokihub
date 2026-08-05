import { CirclePlusIcon, LucideIcon, UsersIcon } from "lucide-react";
import { TFunction } from "i18next";

export type WalletTypeOption = {
  to: string;
  icon: LucideIcon;
  title: string;
  description: string;
};

// Cash Hub is deliberately NOT one of these — it moved out to its own
// top-level page/route (/cash-hub, /cash-hub/new) rather than being one of
// the Sub-wallets type choices, so this chooser is left with the two kinds
// that stayed here.
export function getWalletTypes(t: TFunction<"wallet">): WalletTypeOption[] {
  return [
    {
      to: "/sub-wallets/new/simple",
      icon: CirclePlusIcon,
      title: t("subwallets.walletTypes.simple.title"),
      description: t("subwallets.walletTypes.simple.description"),
    },
    {
      to: "/sub-wallets/new/circle",
      icon: UsersIcon,
      title: t("subwallets.walletTypes.circle.title"),
      description: t("subwallets.walletTypes.circle.description"),
    },
  ];
}
