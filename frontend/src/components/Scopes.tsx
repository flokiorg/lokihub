import {
  ArrowDownUpIcon,
  BrickWallIcon,
  ChevronsUpDownIcon,
  LucideIcon,
  MoveDownIcon,
  SquarePenIcon,
} from "lucide-react";
import React from "react";
import { Link } from "react-router-dom";
import { JITHubConfigCard } from "src/components/JITHubConfigCard";
import { Button } from "src/components/ui/button";
import { Checkbox } from "src/components/ui/checkbox";
import { Label } from "src/components/ui/label";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "src/components/ui/sheet";
import { cn } from "src/lib/utils";
import { Scope, WalletCapabilities, scopeDescriptions } from "src/types";

const scopeGroups = ["full_access", "read_only", "isolated", "custom"] as const;
type ScopeGroup = (typeof scopeGroups)[number];
type ScopeGroupIconMap = { [key in ScopeGroup]: LucideIcon };

const scopeGroupIconMap: ScopeGroupIconMap = {
  full_access: ArrowDownUpIcon,
  read_only: MoveDownIcon,
  isolated: BrickWallIcon,
  custom: SquarePenIcon,
};

const scopeGroupTitle: Record<ScopeGroup, string> = {
  full_access: "Full Access",
  read_only: "Read Only",
  isolated: "Isolated App",
  custom: "Custom",
};

const scopeGroupDescriptions: Record<ScopeGroup, string> = {
  full_access: "Allow this app to send and receive payments from your wallet",
  read_only: "Allow this app to receive payments and view transaction history",
  isolated:
    "Create a separate wallet for this app with its own isolated balance",
  custom: "Define specific permissions for this app's wallet access",
};

// Defaults applied the moment JIT Hub is switched on — same starting values
// as the dedicated Sub-wallets "New JIT Hub" flow (NewJITHub.tsx). Exported
// so callers (NewApp.tsx) can seed the same values into AppPermissions up
// front, since these fields only get a value pushed up via
// onJitHubConfigChanged once the user actually edits one of the inputs.
export const DEFAULT_JIT_PER_WALLET_MAX_LOKI = 1000;
export const DEFAULT_JIT_MAX_EXP_SECS = 86400;

interface ScopesProps {
  capabilities: WalletCapabilities;
  scopes: Scope[];
  isolated: boolean;
  isNewConnection: boolean;
  onScopesChanged: (scopes: Scope[], isolated: boolean) => void;
  // JIT Hub escalation — only ever offered on a brand-new connection (kind is
  // immutable after creation, see AppDetails' own Hub Settings card for
  // editing an existing hub) and only once the connection is isolated, since
  // kind "jit_hub" always carries its own isolated balance server-side.
  jitHub?: boolean;
  jitPerWalletMaxLoki?: number;
  jitMaxExpSecs?: number;
  onJitHubChanged?: (jitHub: boolean) => void;
  onJitHubConfigChanged?: (config: {
    perWalletMaxLoki?: number;
    maxExpSecs?: number;
  }) => void;
}

const Scopes: React.FC<ScopesProps> = ({
  capabilities,
  scopes,
  isolated,
  isNewConnection,
  onScopesChanged,
  jitHub = false,
  jitPerWalletMaxLoki = DEFAULT_JIT_PER_WALLET_MAX_LOKI,
  jitMaxExpSecs = DEFAULT_JIT_MAX_EXP_SECS,
  onJitHubChanged,
  onJitHubConfigChanged,
}) => {
  const [isSheetOpen, setSheetOpen] = React.useState(false);
  const fullAccessScopes: Scope[] = React.useMemo(() => {
    return [...capabilities.scopes];
  }, [capabilities.scopes]);

  const readOnlyScopes: Scope[] = React.useMemo(() => {
    const readOnlyScopes: Scope[] = [
      "get_balance",
      "get_info",
      "make_invoice",
      "lookup_invoice",
      "list_transactions",
      "notifications",
    ];

    return capabilities.scopes.filter((scope) =>
      readOnlyScopes.includes(scope)
    );
  }, [capabilities.scopes]);

  const isolatedScopes: Scope[] = React.useMemo(() => {
    const isolatedScopes: Scope[] = [
      "pay_invoice",
      "get_balance",
      "get_info",
      "make_invoice",
      "lookup_invoice",
      "list_transactions",
      "notifications",
    ];

    return capabilities.scopes.filter((scope) =>
      isolatedScopes.includes(scope)
    );
  }, [capabilities.scopes]);

  const [scopeGroup, setScopeGroup] = React.useState<ScopeGroup>(() => {
    if (
      isolated &&
      scopes.length === isolatedScopes.length &&
      scopes.every((scope) => isolatedScopes.includes(scope))
    ) {
      return "isolated";
    }
    if (
      scopes.length === fullAccessScopes.length &&
      scopes.every((scope) => fullAccessScopes.includes(scope))
    ) {
      return "full_access";
    }
    if (
      scopes.length === readOnlyScopes.length &&
      readOnlyScopes.every((readOnlyScope) => scopes.includes(readOnlyScope))
    ) {
      return "read_only";
    }

    return "custom";
  });

  const handleScopeGroupChange = (scopeGroup: ScopeGroup) => {
    setScopeGroup(scopeGroup);
    switch (scopeGroup) {
      case "full_access":
        onScopesChanged(fullAccessScopes, false);
        break;
      case "read_only":
        onScopesChanged(readOnlyScopes, false);
        break;
      case "isolated":
        onScopesChanged(isolatedScopes, true);
        break;
      default: {
        onScopesChanged([], false);
        break;
      }
    }
  };

  const handleScopeChange = (scope: Scope) => {
    let newScopes = [...scopes];
    if (newScopes.includes(scope)) {
      newScopes = newScopes.filter((existing) => existing !== scope);
    } else {
      newScopes.push(scope);
    }

    onScopesChanged(newScopes, isolated);
  };

  const ActiveScopeGroupIcon = scopeGroupIconMap[scopeGroup];

  return (
    <>
      <Sheet open={isSheetOpen} onOpenChange={setSheetOpen}>
        <SheetContent>
          <SheetHeader>
            <SheetTitle>Choose wallet permissions</SheetTitle>
          </SheetHeader>
          <div className="flex flex-col gap-4 px-6">
            {scopeGroups.map((sg, index) => {
              const ScopeGroupIcon = scopeGroupIconMap[sg];
              return (
                <button
                  type="button"
                  key={index}
                  className={`flex gap-4 items-center border-2 rounded-md cursor-pointer ${scopeGroup == sg ? "border-primary" : "border-muted"} p-4`}
                  onClick={() => {
                    handleScopeGroupChange(sg);
                    setSheetOpen(false);
                  }}
                >
                  <ScopeGroupIcon className="shrink-0 w-6 h-6 mx-2" />
                  <div className="flex flex-col text-start">
                    <p className="font-semibold">{scopeGroupTitle[sg]}</p>
                    <span className="text-sm text-muted-foreground">
                      {scopeGroupDescriptions[sg]}
                    </span>
                  </div>
                </button>
              );
            })}
            {!isNewConnection && (
              <p className="text-xs text-muted-foreground px-1">
                An existing connection can't be upgraded to JIT or Circle
                wallets.{" "}
                <Link
                  to="/sub-wallets/new/jit"
                  className="underline hover:text-foreground"
                  onClick={() => setSheetOpen(false)}
                >
                  Create a JIT Hub
                </Link>{" "}
                or{" "}
                <Link
                  to="/sub-wallets/new/circle"
                  className="underline hover:text-foreground"
                  onClick={() => setSheetOpen(false)}
                >
                  Create a Circle Hub
                </Link>{" "}
                instead.
              </p>
            )}
          </div>
          <SheetFooter>
            <Button type="submit">Save changes</Button>
            <SheetClose asChild>
              <Button variant="outline">Close</Button>
            </SheetClose>
          </SheetFooter>
        </SheetContent>
      </Sheet>
      <div className="flex flex-col w-full mb-4">
        <p className="font-medium text-sm mb-2">Wallet permissions</p>
        <button
          type="button"
          className="flex gap-4 items-center border-2 rounded-md cursor-pointer border-muted p-4"
          onClick={() => {
            setSheetOpen(true);
          }}
        >
          <ActiveScopeGroupIcon className="shrink-0 w-6 h-6 mx-2" />
          <div className="flex flex-col text-left">
            <p className="font-semibold">{scopeGroupTitle[scopeGroup]}</p>
            <span className="text-sm text-muted-foreground">
              {scopeGroupDescriptions[scopeGroup]}
            </span>
          </div>
          <ChevronsUpDownIcon className="w-4 h-4" />
        </button>
      </div>

      {scopeGroup == "custom" && (
        <div className="mb-2">
          <p className="font-medium text-sm mt-4">Isolation</p>
          <div className="flex items-center mt-2">
            <Checkbox
              id="isolated"
              className="me-2"
              onCheckedChange={() => onScopesChanged(scopes, !isolated)}
              checked={isolated}
            />
            <Label htmlFor="isolated" className="cursor-pointer">
              Isolate this app's balance and transactions
            </Label>
          </div>
          <p className="font-medium text-sm mt-4">Authorize the app to:</p>
          <ul className="flex flex-col w-full mt-2">
            {capabilities.scopes.map((scope, index) => {
              return (
                <li
                  key={index}
                  className={cn(
                    "w-full",
                    scope == "pay_invoice" ? "order-last" : ""
                  )}
                >
                  <div className="flex items-center mb-2">
                    <Checkbox
                      id={scope}
                      className="me-2"
                      onCheckedChange={() => handleScopeChange(scope)}
                      checked={scopes.includes(scope)}
                    />
                    <Label htmlFor={scope} className="cursor-pointer">
                      {scopeDescriptions[scope]}
                    </Label>
                  </div>
                </li>
              );
            })}
          </ul>
        </div>
      )}

      {isNewConnection && isolated && onJitHubChanged && (
        <div className="mb-2 border rounded-md p-4">
          <p className="font-medium text-sm mb-2">JIT Hub (optional)</p>
          <div className="flex items-center">
            <Checkbox
              id="jitHub"
              className="me-2"
              onCheckedChange={() => onJitHubChanged(!jitHub)}
              checked={jitHub}
            />
            <Label htmlFor="jitHub" className="cursor-pointer">
              Also allow this app to create JIT wallets, paying third parties
              directly from its balance
            </Label>
          </div>
          {jitHub && onJitHubConfigChanged && (
            <div className="mt-3">
              <JITHubConfigCard
                budgetLabel="Max Wallet Budget"
                budgetHelper="Maximum budget that can be allocated to each JIT wallet issued from this connection"
                expiryLabel="Max Wallet Expiry"
                expiryHelper="Maximum lifetime for issued JIT wallets"
                perWalletMaxLoki={jitPerWalletMaxLoki}
                onPerWalletMaxLokiChange={(perWalletMaxLoki) =>
                  onJitHubConfigChanged({ perWalletMaxLoki })
                }
                maxExpSecs={jitMaxExpSecs}
                onMaxExpSecsChange={(maxExpSecs) =>
                  onJitHubConfigChanged({ maxExpSecs })
                }
              />
            </div>
          )}
        </div>
      )}
    </>
  );
};

export default Scopes;
