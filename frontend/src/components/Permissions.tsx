import { AlertTriangleIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { BudgetExpiryFields } from "src/components/BudgetExpiryFields";
import Scopes from "src/components/Scopes";
import { Badge } from "src/components/ui/badge";
import { cn } from "src/lib/utils";
import {
  AppPermissions,
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
  // Lets a caller opt a non-pay_invoice app (e.g. a circle_hub, which is
  // never granted pay_invoice) into the budget UI below. Defaults to the
  // usual pay_invoice-scope check so existing callers are unaffected.
  showBudgetSection?: boolean;
  // Optional note rendered directly under the budget field, e.g. to explain
  // what this budget covers for a circle_hub.
  budgetCaption?: React.ReactNode;
  // When true (default) Permissions renders the Budget & expiry fields inline
  // after the scopes — the behaviour NewApp / AppDetails rely on. The hub
  // dashboards pass false and render <BudgetExpiryFields/> as their own group
  // so expiry/renewal don't read as "permissions".
  showBudgetAndExpiry?: boolean;
  // Names the thing these permissions belong to in "This {{kind}} is
  // authorized to:" below — defaults to "App" (NewApp.tsx's case: always a
  // real third-party connection), but AppDetails.tsx passes the actual
  // appKindLabel for every kind, since this same component is shown for a
  // user's own Cash Hub/Circle Hub/Cash Wallet/Circle Wallet too.
  kindLabel?: string;
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
  showBudgetSection = permissions.scopes.includes("pay_invoice"),
  budgetCaption,
  kindLabel,
  showBudgetAndExpiry = true,
}) => {
  const { t } = useTranslation("apps");

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
      // Un-isolating always drops any pending Cash Hub escalation with it —
      // kind "cash_hub" cannot exist without isolation server-side (see
      // Scopes.tsx), so keep local state from drifting into that impossible
      // combination even transiently.
      handlePermissionsChange({
        scopes,
        isolated,
        ...(!isolated && { cashHub: false }),
      });
    },
    [handlePermissionsChange]
  );

  const onCashHubChanged = React.useCallback(
    (cashHub: boolean) => {
      handlePermissionsChange({ cashHub });
    },
    [handlePermissionsChange]
  );

  const onCashHubConfigChanged = React.useCallback(
    (config: { perWalletMaxLoki?: number; maxExpSecs?: number }) => {
      handlePermissionsChange({
        ...(config.perWalletMaxLoki !== undefined && {
          cashPerWalletMaxLoki: config.perWalletMaxLoki,
        }),
        ...(config.maxExpSecs !== undefined && {
          cashMaxExpSecs: config.maxExpSecs,
        }),
      });
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
          cashHub={permissions.cashHub}
          cashPerWalletMaxLoki={permissions.cashPerWalletMaxLoki}
          cashMaxExpSecs={permissions.cashMaxExpSecs}
          onCashHubChanged={onCashHubChanged}
          onCashHubConfigChanged={onCashHubConfigChanged}
        />
      ) : (
        <>
          <p className="text-sm font-medium mb-2">
            {t("permissions.authorizedTo", {
              kind: kindLabel ?? t("kindLabel.app"),
              defaultValue: "This {{kind}} is authorized to:",
            })}
          </p>
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
                  <p className="text-sm">
                    {t(`scopes.${scope}`, { defaultValue: scope })}
                  </p>
                </Badge>
              );
            })}
          </div>
        </>
      )}

      {/* Budget & expiry — rendered inline by default (NewApp / AppDetails),
          but the hub dashboards pass showBudgetAndExpiry={false} and render
          <BudgetExpiryFields/> as their own labelled group instead. */}
      {showBudgetAndExpiry && (
        <BudgetExpiryFields
          permissions={permissions}
          setPermissions={setPermissions}
          readOnly={readOnly}
          budgetReadOnly={budgetReadOnly}
          expiresAtReadOnly={expiresAtReadOnly}
          budgetUsage={budgetUsage}
          isNewConnection={isNewConnection}
          showBudgetUsage={showBudgetUsage}
          showBudgetSection={showBudgetSection}
          budgetCaption={budgetCaption}
        />
      )}

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
