import dayjs from "dayjs";
import {
  BrickWallIcon,
  CircleCheckIcon,
  NetworkIcon,
  PlusCircleIcon,
  UsersIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { LinkButton } from "src/components/ui/custom/link-button";
import { Progress } from "src/components/ui/progress";
import { SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { getBudgetRenewalLabel } from "src/lib/utils";
import { App } from "src/types";

type AppCardConnectionInfoProps = {
  connection: App;
  budgetRemainingText?: string | React.ReactNode;
  readonly?: boolean;
};

// Isolated apps span several distinct kinds now (plain sub-wallets, JIT
// Hubs/Wallets, Circle Hubs/Wallets) — label and icon must reflect the
// actual kind rather than collapsing everything into "Sub-wallet".
const isolatedKindDisplay: Record<
  string,
  {
    Icon: typeof BrickWallIcon;
    labelKey:
      | "isolatedKind.jit_hub"
      | "isolatedKind.jit_wallet"
      | "isolatedKind.circle_hub"
      | "isolatedKind.circle_wallet";
  }
> = {
  jit_hub: { Icon: NetworkIcon, labelKey: "isolatedKind.jit_hub" },
  jit_wallet: { Icon: NetworkIcon, labelKey: "isolatedKind.jit_wallet" },
  circle_hub: { Icon: UsersIcon, labelKey: "isolatedKind.circle_hub" },
  circle_wallet: { Icon: UsersIcon, labelKey: "isolatedKind.circle_wallet" },
};

export function AppCardConnectionInfo({
  connection,
  budgetRemainingText,
  readonly = false,
}: AppCardConnectionInfoProps) {
  const { t } = useTranslation("apps");
  const resolvedBudgetRemainingText =
    budgetRemainingText ?? t("usage.leftInBudget", "Left in budget");
  const isolatedDisplay = connection.kind
    ? isolatedKindDisplay[connection.kind]
    : undefined;
  const IsolatedIcon = isolatedDisplay?.Icon ?? BrickWallIcon;
  const isolatedLabel = isolatedDisplay
    ? t(isolatedDisplay.labelKey)
    : connection.metadata?.app_store_app_id === SUBWALLET_APPSTORE_APP_ID
      ? t("isolatedKind.subwallet")
      : t("isolatedKind.isolatedApp");

  return (
    <>
      {connection.isolated ? (
        <>
          <div className="text-sm text-secondary-foreground font-medium w-full h-full flex flex-col gap-2">
            <div className="flex flex-row items-center gap-2">
              <IsolatedIcon className="size-4" />
              {isolatedLabel}
            </div>
          </div>
          <div className="flex flex-row justify-between text-xs items-end mt-2">
            <div className="text-muted-foreground">
              Last used:{" "}
              {connection.lastUsedAt
                ? dayjs(connection.lastUsedAt).fromNow()
                : "Never"}
            </div>
            <div className="flex flex-col items-end justify-end">
              <p>Balance</p>
              <p className="text-xl font-medium">
                <FormattedFlokicoinAmount amount={connection.balance} />
              </p>
            </div>
          </div>
        </>
      ) : connection.maxAmount > 0 ? (
        <>
          <div className="flex flex-row justify-between">
            <div className="mb-2">
              <p className="text-xs text-secondary-foreground font-medium">
                {resolvedBudgetRemainingText}
              </p>
              <p className="text-xl font-medium">
                <FormattedFlokicoinAmount
                  amount={
                    (connection.maxAmount - connection.budgetUsage) * 1000
                  }
                />
              </p>
            </div>
          </div>
          <Progress
            className="h-4"
            value={100 - (connection.budgetUsage * 100) / connection.maxAmount}
          />
          <div className="flex flex-row justify-between text-xs items-center text-muted-foreground mt-2">
            <div>
              Last used:{" "}
              {connection.lastUsedAt
                ? dayjs(connection.lastUsedAt).fromNow()
                : "Never"}
            </div>
            <div>
              {connection.maxAmount && (
                <>
                  <FormattedFlokicoinAmount
                    amount={connection.maxAmount * 1000}
                  />
                  {connection.budgetRenewal !== "never" && (
                    <> / {getBudgetRenewalLabel(connection.budgetRenewal)}</>
                  )}
                </>
              )}
            </div>
          </div>
        </>
      ) : connection.scopes.indexOf("pay_invoice") > -1 ? (
        <>
          <div className="flex flex-row justify-between">
            <div className="mb-2">
              <p className="text-xs text-secondary-foreground font-medium">
                You've spent
              </p>
              <p className="text-xl font-medium">
                <FormattedFlokicoinAmount
                  amount={connection.budgetUsage * 1000}
                />
              </p>
            </div>
          </div>
          <div className="flex flex-row justify-between items-center">
            <div className="text-muted-foreground text-xs">
              Last used:{" "}
              {connection.lastUsedAt
                ? dayjs(connection.lastUsedAt).fromNow()
                : "Never"}
            </div>
            {!readonly && (
              <LinkButton
                to={`/apps/${connection.id}?edit=true`}
                variant="outline"
              >
                <PlusCircleIcon />
                Set Budget
              </LinkButton>
            )}
          </div>
        </>
      ) : (
        <>
          <div className="text-sm text-secondary-foreground font-medium w-full h-full flex flex-col gap-2">
            <div className="flex flex-row items-center gap-2">
              <CircleCheckIcon className="size-4" />
              Share wallet information
            </div>
            {connection.scopes.indexOf("make_invoice") > -1 && (
              <div className="flex flex-row items-center gap-2">
                <CircleCheckIcon className="size-4" />
                Receive payments
              </div>
            )}
            {connection.scopes.indexOf("list_transactions") > -1 && (
              <div className="flex flex-row items-center gap-2">
                <CircleCheckIcon className="size-4" />
                Read transaction history
              </div>
            )}
          </div>
          <div className="flex flex-row justify-between items-center">
            <div className="flex flex-row justify-between text-xs items-center text-muted-foreground">
              Last used:{" "}
              {connection.lastUsedAt
                ? dayjs(connection.lastUsedAt).fromNow()
                : "Never"}
            </div>
            {!readonly && (
              <LinkButton
                to={`/apps/${connection.id}?edit=true`}
                variant="outline"
              >
                <PlusCircleIcon />
                Enable Payments
              </LinkButton>
            )}
          </div>
        </>
      )}
    </>
  );
}
