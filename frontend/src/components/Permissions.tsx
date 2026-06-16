import { AlertTriangleIcon, PlusCircleIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import BudgetAmountSelect from "src/components/BudgetAmountSelect";
import BudgetRenewalSelect from "src/components/BudgetRenewalSelect";
import ExpirySelect from "src/components/ExpirySelect";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Scopes from "src/components/Scopes";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import {
  DEFAULT_APP_BUDGET_LOKI,
  DEFAULT_APP_BUDGET_RENEWAL
} from "src/constants";
import { cn } from "src/lib/utils";
import {
  AppPermissions,
  BudgetRenewalType,
  Scope,
  WalletCapabilities,
  scopeIconMap,
} from "src/types";

interface PermissionsProps {
  capabilities: WalletCapabilities;
  permissions: AppPermissions;
  setPermissions?: React.Dispatch<React.SetStateAction<AppPermissions>>;
  readOnly?: boolean;
  scopesReadOnly?: boolean;
  budgetReadOnly?: boolean;
  expiresAtReadOnly?: boolean;
  budgetUsage?: number;
  isNewConnection: boolean;
  showBudgetUsage?: boolean;
}

const Permissions: React.FC<PermissionsProps> = ({
  capabilities,
  permissions,
  setPermissions,
  isNewConnection,
  budgetUsage,
  readOnly,
  scopesReadOnly,
  budgetReadOnly,
  expiresAtReadOnly,
  showBudgetUsage = true,
}) => {
  const { t } = useTranslation("apps");
  const [showBudgetOptions, setShowBudgetOptions] = React.useState(
    permissions.scopes.includes("pay_invoice") && permissions.maxAmount > 0
  );
  const [showExpiryOptions, setShowExpiryOptions] = React.useState(
    !!permissions.expiresAt
  );

  const handlePermissionsChange = React.useCallback(
    (changedPermissions: Partial<AppPermissions>) => {
      setPermissions?.((currentPermissions) => ({
        ...currentPermissions,
        ...changedPermissions,
      }));
    },
    [setPermissions]
  );

  const onScopesChanged = React.useCallback(
    (scopes: Scope[], isolated: boolean) => {
      handlePermissionsChange({ scopes, isolated });
    },
    [handlePermissionsChange]
  );

  const handleBudgetMaxAmountChange = React.useCallback(
    (amount: number) => {
      handlePermissionsChange({ maxAmount: amount });
    },
    [handlePermissionsChange]
  );

  const handleBudgetRenewalChange = React.useCallback(
    (budgetRenewal: BudgetRenewalType) => {
      handlePermissionsChange({ budgetRenewal });
    },
    [handlePermissionsChange]
  );

  const handleExpiryChange = React.useCallback(
    (expiryDate?: Date) => {
      handlePermissionsChange({ expiresAt: expiryDate });
    },
    [handlePermissionsChange]
  );

  return (
    <div className={cn(!readOnly && "max-w-lg")}>
      {!readOnly && !scopesReadOnly ? (
        <Scopes
          capabilities={capabilities}
          scopes={permissions.scopes}
          isolated={permissions.isolated}
          onScopesChanged={onScopesChanged}
          isNewConnection={isNewConnection}
        />
      ) : (
        <>
          <p className="text-sm font-medium mb-2">{t("permissions.authorizedTo")}</p>
          <div className="flex flex-wrap gap-2 mb-4">
            {[...permissions.scopes].map((scope) => {
              const PermissionIcon = scopeIconMap[scope];
              return (
                <Badge
                  variant="secondary"
                  key={scope}
                  className={cn(
                    "flex items-center font-normal py-1 rounded-full px-3"
                  )}
                >
                  <PermissionIcon className="me-1 size-4" />
                  <p className="text-sm">{t(`scopes.${scope}`, { defaultValue: scope })}</p>
                </Badge>
              );
            })}
          </div>
        </>
      )}

      {permissions.scopes.includes("pay_invoice") && showBudgetUsage && (
        <>
          {!readOnly && !budgetReadOnly ? (
            <>
              {!showBudgetOptions && (
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => {
                    handleBudgetRenewalChange(DEFAULT_APP_BUDGET_RENEWAL);
                    handleBudgetMaxAmountChange(DEFAULT_APP_BUDGET_LOKI);
                    setShowBudgetOptions(true);
                  }}
                  className={cn("me-4", showExpiryOptions && "mb-4")}
                >
                  <PlusCircleIcon />
                  {t("budget.set")}
                </Button>
              )}
              {showBudgetOptions && (
                <>
                  <BudgetRenewalSelect
                    value={permissions.budgetRenewal}
                    onChange={handleBudgetRenewalChange}
                    onClose={() => {
                      handleBudgetRenewalChange("never");
                      handleBudgetMaxAmountChange(0);
                      setShowBudgetOptions(false);
                    }}
                  />
                  <BudgetAmountSelect
                    value={permissions.maxAmount}
                    onChange={handleBudgetMaxAmountChange}
                  />
                </>
              )}
            </>
          ) : (
            <div className="ps-4 ms-2 border-s-2 border-s-primary mb-4">
              <div className="flex flex-col gap-2 text-muted-foreground text-sm">
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
        </>
      )}

      <>
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
                onChange={handleExpiryChange}
              />
            )}
          </>
        ) : (
          <>
            <p className="text-sm font-medium mb-2">{t("expiry.connectionExpiry")}</p>
            <p className="text-muted-foreground text-sm">
              {permissions.expiresAt
                ? new Date(permissions.expiresAt).toString()
                : t("expiry.neverExpires")}
            </p>
          </>
        )}
      </>

      {permissions.scopes.includes("superuser") && (
        <>
          <div className="flex items-center gap-2 mt-4">
            <AlertTriangleIcon className="size-4" />
            <p className="text-sm font-medium">
              {t("permissions.superuserTitle")}
            </p>
          </div>
          <p className="text-muted-foreground text-sm">
            {t("permissions.superuserDesc")}
          </p>
        </>
      )}
    </div>
  );
};

export default Permissions;
