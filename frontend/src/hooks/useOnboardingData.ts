// src/hooks/useOnboardingData.ts

import { useTranslation } from "react-i18next";
import { LOKI_ACCOUNT_APP_NAME } from "src/constants";
import { useApps } from "src/hooks/useApps";
import { useChannels } from "src/hooks/useChannels";
import { useInfo } from "src/hooks/useInfo";

import { useNodeConnectionInfo } from "src/hooks/useNodeConnectionInfo";
import { useTransactions } from "src/hooks/useTransactions";

interface ChecklistItem {
  title: string;
  description: string;
  checked: boolean;
  to: string;
  disabled: boolean;
}

interface UseOnboardingDataResponse {
  isLoading: boolean;
  checklistItems: ChecklistItem[];
}

export const useOnboardingData = (): UseOnboardingDataResponse => {
  const { t } = useTranslation("wallet");

  const { data: appsData } = useApps();
  const { data: channels } = useChannels();
  const { data: info, hasChannelManagement, hasMnemonic } = useInfo();
  const { data: nodeConnectionInfo } = useNodeConnectionInfo();
  const { data: transactions } = useTransactions(undefined, false, 1);

  const isLoading =
    !appsData ||
    !channels ||
    !info ||
    !nodeConnectionInfo ||
    !transactions ||
    !transactions;

  if (isLoading) {
    return { isLoading: true, checklistItems: [] };
  }

  const hasChannel =
    !hasChannelManagement || (hasChannelManagement && channels.length > 0);
  const hasBackedUp = hasMnemonic === true;
  const hasCustomApp =
    appsData &&
    appsData.apps.find((x) => x.name !== LOKI_ACCOUNT_APP_NAME) !== undefined;
  const hasTransaction = transactions.totalCount > 0;

  const checklistItems: Omit<ChecklistItem, "disabled">[] = [
    ...(hasChannelManagement
      ? [
          {
            title: t("onboarding.items.openChannel.title"),
            description: t("onboarding.items.openChannel.description"),
            checked: hasChannel,
            to: "/channels/first",
          },
        ]
      : []),
    {
      title: t("onboarding.items.firstPayment.title"),
      description: t("onboarding.items.firstPayment.description"),
      checked: hasTransaction,
      to: "/wallet",
    },
    {
      title: t("onboarding.items.connectApp.title"),
      description: t("onboarding.items.connectApp.description"),
      checked: hasCustomApp,
      to: "/apps?tab=app-store",
    },
    ...(hasMnemonic
      ? [
          {
            title: t("onboarding.items.backupKeys.title"),
            description: t("onboarding.items.backupKeys.description"),
            checked: hasBackedUp === true,
            to: "/settings/backup",
          },
        ]
      : []),
  ];

  const nextStep = checklistItems.find((x) => !x.checked);

  const sortedChecklistItems = checklistItems.map((item) => ({
    ...item,
    disabled: item !== nextStep,
  }));

  return { isLoading: false, checklistItems: sortedChecklistItems };
};
