import { PlusCircleIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import BudgetAmountSelect from "src/components/BudgetAmountSelect";
import BudgetRenewalSelect from "src/components/BudgetRenewalSelect";
import ExpirySelect from "src/components/ExpirySelect";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { Button } from "src/components/ui/button";
import {
  DEFAULT_APP_BUDGET_LOKI,
  DEFAULT_APP_BUDGET_RENEWAL,
} from "src/constants";
import { AppPermissions, BudgetRenewalType } from "src/types";

// Budget + renewal + expiry, split out of Permissions so they read as their own
// "Budget & expiry" group rather than as a connection's *permissions*. Used
// inline by Permissions (its default, to keep NewApp / AppDetails unchanged) and
// standalone by the hub dashboards, which render it as a separate group card.
export function BudgetExpiryFields({
  permissions,
  setPermissions,
  readOnly,
  budgetReadOnly,
  expiresAtReadOnly,
  budgetUsage,
  isNewConnection,
  showBudgetUsage = true,
  showBudgetSection = permissions.scopes.includes("pay_invoice"),
  budgetCaption,
}: {
  permissions: AppPermissions;
  setPermissions?: React.Dispatch<React.SetStateAction<AppPermissions>>;
  readOnly?: boolean;
  budgetReadOnly?: boolean;
  expiresAtReadOnly?: boolean;
  budgetUsage?: number;
  isNewConnection: boolean;
  showBudgetUsage?: boolean;
  showBudgetSection?: boolean;
  budgetCaption?: React.ReactNode;
}) {
  const { t } = useTranslation("apps");
  const [showBudgetOptions, setShowBudgetOptions] = React.useState(
    showBudgetSection && permissions.maxAmount > 0
  );
  const [showExpiryOptions, setShowExpiryOptions] = React.useState(
    !!permissions.expiresAt
  );

  const change = React.useCallback(
    (changed: Partial<AppPermissions>) => {
      setPermissions?.((current) => ({ ...current, ...changed }));
    },
    [setPermissions]
  );
  const onRenewal = React.useCallback(
    (budgetRenewal: BudgetRenewalType) => change({ budgetRenewal }),
    [change]
  );
  const onMaxAmount = React.useCallback(
    (maxAmount: number) => change({ maxAmount }),
    [change]
  );
  const onExpiry = React.useCallback(
    (expiresAt?: Date) => change({ expiresAt }),
    [change]
  );

  return (
    <div className={readOnly ? undefined : "max-w-lg"}>
      {showBudgetSection && showBudgetUsage && (
        <>
          {!readOnly && !budgetReadOnly ? (
            <>
              {!showBudgetOptions && (
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => {
                    onRenewal(DEFAULT_APP_BUDGET_RENEWAL);
                    onMaxAmount(DEFAULT_APP_BUDGET_LOKI);
                    setShowBudgetOptions(true);
                  }}
                  className={showExpiryOptions ? "me-4 mb-4" : "me-4"}
                >
                  <PlusCircleIcon />
                  {t("budget.set")}
                </Button>
              )}
              {showBudgetOptions && (
                <>
                  <BudgetRenewalSelect
                    value={permissions.budgetRenewal}
                    onChange={onRenewal}
                    onClose={() => {
                      onRenewal("never");
                      onMaxAmount(0);
                      setShowBudgetOptions(false);
                    }}
                  />
                  <BudgetAmountSelect
                    value={permissions.maxAmount}
                    onChange={onMaxAmount}
                  />
                </>
              )}
            </>
          ) : (
            <div className="border-s-primary mb-4 ms-2 border-s-2 ps-4">
              <div className="text-muted-foreground flex flex-col gap-2 text-sm">
                <p className="capitalize">
                  <span className="text-primary font-medium">
                    {t("budget.renewal")}:
                  </span>{" "}
                  {t(`renewals.${permissions.budgetRenewal || "never"}`)}
                </p>
                <p className="slashed-zero">
                  <span className="text-primary font-medium">
                    {t("budget.amount")}:
                  </span>{" "}
                  {permissions.maxAmount ? (
                    <FormattedFlokicoinAmount
                      amount={permissions.maxAmount * 1000}
                    />
                  ) : (
                    "∞"
                  )}{" "}
                  {!isNewConnection && (
                    <>
                      (
                      <FormattedFlokicoinAmount
                        amount={(budgetUsage || 0) * 1000}
                      />{" "}
                      {t("budget.used")})
                    </>
                  )}
                </p>
              </div>
            </div>
          )}
          {budgetCaption && (
            <p className="text-muted-foreground -mt-2 text-sm">
              {budgetCaption}
            </p>
          )}
        </>
      )}

      {!readOnly && !expiresAtReadOnly ? (
        <>
          {!showExpiryOptions && (
            <Button
              type="button"
              variant="secondary"
              onClick={() => setShowExpiryOptions(true)}
            >
              <PlusCircleIcon />
              {t("expiry.set")}
            </Button>
          )}
          {showExpiryOptions && (
            <ExpirySelect
              value={permissions.expiresAt}
              onChange={onExpiry}
            />
          )}
        </>
      ) : (
        <>
          <p className="mb-2 text-sm font-medium">
            {t("expiry.connectionExpiry")}
          </p>
          <p className="text-muted-foreground text-sm">
            {permissions.expiresAt
              ? new Date(permissions.expiresAt).toString()
              : t("expiry.neverExpires")}
          </p>
        </>
      )}
    </div>
  );
}

export default BudgetExpiryFields;
