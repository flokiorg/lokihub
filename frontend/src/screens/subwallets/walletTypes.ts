import { CirclePlusIcon, LucideIcon, NetworkIcon, UsersIcon } from "lucide-react";
import { TFunction } from "i18next";

export type WalletTypeOption = {
  to: string;
  icon: LucideIcon;
  title: string;
  description: string;
};

export function getWalletTypes(t: TFunction<"wallet">): WalletTypeOption[] {
  return [
    {
      to: "/sub-wallets/new/simple",
      icon: CirclePlusIcon,
      title: t("subwallets.walletTypes.simple.title"),
      description: t("subwallets.walletTypes.simple.description"),
    },
    {
      to: "/sub-wallets/new/jit",
      icon: NetworkIcon,
      title: t("subwallets.walletTypes.jit.title"),
      description: t("subwallets.walletTypes.jit.description"),
    },
    {
      to: "/sub-wallets/new/circle",
      icon: UsersIcon,
      title: t("subwallets.walletTypes.circle.title"),
      description: t("subwallets.walletTypes.circle.description"),
    },
  ];
}
