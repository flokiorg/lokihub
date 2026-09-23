import {
  CheckCircleIcon,
  ChevronDownIcon,
  EllipsisIcon,
  InfoIcon,
  PlusIcon,
  SquarePenIcon,
  Trash2Icon,
  TriangleAlertIcon,
} from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { Link, Navigate, useParams } from "react-router-dom";
import { toast } from "sonner";
import AppAvatar from "src/components/AppAvatar";
import AppHeader from "src/components/AppHeader";
import {
  CashFlowChart,
  CashOutstandingChart,
} from "src/components/cash/CashFlowChart";
import { CashHubOverview } from "src/components/cash/CashHubOverview";
import { CashHubConfigCard } from "src/components/CashHubConfigCard";
import { AppTransactionList } from "src/components/connections/AppTransactionList";
import { ConnectionDetailsModal } from "src/components/connections/ConnectionDetailsModal";
import { DisconnectCashHub } from "src/components/connections/DisconnectCashHub";
import Loading from "src/components/Loading";
import Permissions from "src/components/Permissions";
import ResponsiveButton from "src/components/ResponsiveButton";
import { Alert, AlertDescription } from "src/components/ui/alert";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "src/components/ui/accordion";
import { Badge } from "src/components/ui/badge";
import { Checkbox } from "src/components/ui/checkbox";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "src/components/ui/dropdown-menu";
import { Input } from "src/components/ui/input";
import {
  localStorageKeys,
  SUBWALLET_APPSTORE_APP_ID,
} from "src/constants";
import { useApp } from "src/hooks/useApp";
import { useSiblingHubs } from "src/hooks/useApps";
import { useCapabilities } from "src/hooks/useCapabilities";
import { useCashHubStats } from "src/hooks/useCashHub";
import {
  CashHubAllocations,
  CashHubAllocationsHandle,
} from "src/screens/subwallets/CashHubAllocations";
import {
  App,
  AppPermissions,
  CashHubStats,
  UpdateAppRequest,
  WalletCapabilities,
} from "src/types";
import { cn } from "src/lib/utils";
import { appKindLabel, appKindSiblingsLabel } from "src/utils/appKind";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

export function CashHubDashboard() {
  const { id } = useParams() as { id: string };
  const hubId = Number(id);
  const { data: hub, mutate: refetchHub, error } = useApp(hubId);
  const { data: capabilities } = useCapabilities();
  const { data: stats } = useCashHubStats(hubId);

  if (error) {
    return <p className="text-red-500">{error.message}</p>;
  }

  if (!hub || !capabilities) {
    return <Loading />;
  }

  // /cash-hub/:id is reachable with any app id. Without this, a non-hub id
  // would sit on <Loading /> forever, because the cash-stats endpoint it
  // waits on only exists for a cash_hub — and if it ever did render it
  // would offer "Delete Cash Hub" for something that is not one.
  if (hub.kind !== "cash_hub") {
    return <Navigate to={`/apps/${hub.id}`} replace />;
  }

  if (!stats) {
    return <Loading />;
  }

  return (
    <CashHubDashboardInternal
      key={hub.id}
      hub={hub}
      capabilities={capabilities}
      stats={stats}
      refetchHub={refetchHub}
    />
  );
}

type CashHubDashboardInternalProps = {
  hub: App;
  capabilities: WalletCapabilities;
  stats: CashHubStats;
  refetchHub: () => void;
};

// This page is the Cash Hub's *only* home (CashHubList links here, and
// SubwalletList deliberately filters cash_hub out of its own listing). It
// therefore has to carry everything AppDetails offers a hub — permissions,
// hub settings, connection details, delete, transactions — and not just the
// cash-specific analytics it was originally split out to make room for.
function CashHubDashboardInternal({
  hub,
  capabilities,
  stats,
  refetchHub,
}: CashHubDashboardInternalProps) {
  const { t } = useTranslation("apps");
  const { t: tc } = useTranslation("common");
  const { t: tcircles } = useTranslation("circles");

  const allocationsRef = React.useRef<CashHubAllocationsHandle>(null);
  const [isCashFormOpen, setCashFormOpen] = React.useState(false);
  const [isEditing, setIsEditing] = React.useState(false);
  const [showConnectionDetails, setShowConnectionDetails] =
    React.useState(false);
  const [showDeleteDialog, setShowDeleteDialog] = React.useState(false);

  // "Keep open" is the stored default for the Analytics section, shared by
  // every Cash Hub. Expanding or collapsing it by hand is just this visit
  // and does not touch the preference — otherwise the checkbox would fight
  // the disclosure it sits next to.
  const [keepAnalyticsOpen, setKeepAnalyticsOpen] = React.useState(
    () => localStorage.getItem(localStorageKeys.cashAnalyticsKeepOpen) === "true"
  );
  const [isAnalyticsOpen, setAnalyticsOpen] = React.useState(keepAnalyticsOpen);

  const toggleKeepAnalyticsOpen = (checked: boolean) => {
    setKeepAnalyticsOpen(checked);
    localStorage.setItem(
      localStorageKeys.cashAnalyticsKeepOpen,
      String(checked)
    );
  };

  const [name, setName] = React.useState(hub.name);
  const [permissions, setPermissions] = React.useState<AppPermissions>({
    scopes: hub.scopes,
    maxAmount: hub.maxAmount,
    budgetRenewal: hub.budgetRenewal,
    expiresAt: hub.expiresAt ? new Date(hub.expiresAt) : undefined,
    isolated: hub.isolated,
  });
  const [savedPermissions, setSavedPermissions] =
    React.useState<AppPermissions>(permissions);

  const [cashPerWalletMaxLoki, setCashPerWalletMaxLoki] = React.useState(
    hub.cashPerWalletMaxMloki ? hub.cashPerWalletMaxMloki / 1000 : 0
  );
  const [cashMaxExpSecs, setCashMaxExpSecs] = React.useState(
    hub.cashMaxExpSecs ?? 0
  );
  const [cashMinTransferLoki, setCashMinTransferLoki] = React.useState(
    hub.cashMinTransferMloki ? hub.cashMinTransferMloki / 1000 : 0
  );
  const [cashRedeemFeePpm, setCashRedeemFeePpm] = React.useState(
    hub.cashRedeemFeePpm ?? 0
  );

  const kindLabel = appKindLabel("cash_hub");

  // True when a bill minted right now could outlive the hub connection that
  // minted it — including the "never" ceiling (cashMaxExpSecs === 0), where
  // every bill outlives any finite hub expiry.
  //
  // Derived in an effect rather than a useMemo because it reads the clock:
  // Date.now() is impure, so calling it during render is both flagged by the
  // compiler's purity rule and genuinely wrong here — the answer would
  // silently change on any unrelated re-render.
  const [hubExpiresBeforeBills, setHubExpiresBeforeBills] =
    React.useState(false);
  React.useEffect(() => {
    const expiresAt = permissions.expiresAt;
    setHubExpiresBeforeBills(
      !!expiresAt &&
        (cashMaxExpSecs === 0 ||
          expiresAt.getTime() < Date.now() + cashMaxExpSecs * 1000)
    );
  }, [permissions.expiresAt, cashMaxExpSecs]);

  // The other Cash Hubs, for the switcher in the title — same affordance
  // AppDetails offers, but pointing at /cash-hub/:id (this page) rather than
  // /apps/:id, so switching hubs doesn't silently drop the user back onto the
  // generic connection screen they just left.
  //
  // Guarded on the subwallet sentinel exactly as AppDetails' isSubwalletHub
  // is: useSiblingHubs filters by that app_store_app_id, so a cash_hub
  // created through the generic connect flow's "Cash Hub" toggle is absent
  // from the very list it would be offered — listing other people's hubs
  // beside it, without itself, reads as a broken switcher.
  const isManagedHub =
    hub.metadata?.app_store_app_id === SUBWALLET_APPSTORE_APP_ID;
  const siblingHubs = useSiblingHubs(isManagedHub ? "cash_hub" : undefined);

  // A cash_hub's scopes are system-managed (IsPrivilegedKind in
  // db/models.go rejects a scope change on this path), but its own budget
  // and expiry are not (cash_hub is excluded from IsBudgetImmutableKind).
  // That combination is why there is no "are you sure" dialog around Save
  // the way AppDetails has one: neither of the two changes it guards
  // against — granting pay_invoice, dropping isolated — is reachable from
  // here, since both live in the scopes half that cannot move.
  const handleSave = async () => {
    try {
      const updateAppRequest: UpdateAppRequest = {
        name,
        budgetRenewal: permissions.budgetRenewal,
        expiresAt: permissions.expiresAt?.toISOString(),
        updateExpiresAt: true,
        maxAmount: permissions.maxAmount,
        cashPerWalletMaxMloki: cashPerWalletMaxLoki * 1000,
        cashMaxExpSecs,
        cashMinTransferMloki: cashMinTransferLoki * 1000,
        cashRedeemFeePpm,
      };

      await request(`/api/apps/${hub.id}`, {
        method: "PATCH",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify(updateAppRequest),
      });

      refetchHub();
      setIsEditing(false);
      setSavedPermissions(permissions);
      toast(t("connections.successfullyUpdated", "Successfully updated connection"));
    } catch (error) {
      handleRequestError(
        t("connections.failedToUpdate", "Failed to update connection"),
        error
      );
    }
  };

  const cancelEditing = () => {
    // Reset every edited field, not just the flag — otherwise reopening the
    // editor shows the abandoned values as if they had been saved.
    setName(hub.name);
    setPermissions(savedPermissions);
    setCashPerWalletMaxLoki(
      hub.cashPerWalletMaxMloki ? hub.cashPerWalletMaxMloki / 1000 : 0
    );
    setCashMaxExpSecs(hub.cashMaxExpSecs ?? 0);
    setCashMinTransferLoki(
      hub.cashMinTransferMloki ? hub.cashMinTransferMloki / 1000 : 0
    );
    setCashRedeemFeePpm(hub.cashRedeemFeePpm ?? 0);
    setIsEditing(false);
  };

  // Rendered in both modes but in different places: while editing it is one
  // of the three things being edited, so it sits with them near the top;
  // while reading it is settled configuration, so it sits below the bills,
  // the trends and the payments an operator actually came here for.
  const permissionsCard = (
    <Card>
      <CardHeader>
        <CardTitle>{t("permissions.title", "Permissions")}</CardTitle>
      </CardHeader>
      <CardContent>
        {isEditing && (
          <p className="text-sm text-muted-foreground mb-4">
            {t("circleHub.budgetManagedNote")}
          </p>
        )}
        <Permissions
          capabilities={capabilities}
          permissions={isEditing ? permissions : savedPermissions}
          setPermissions={setPermissions}
          readOnly={!isEditing}
          scopesReadOnly
          isNewConnection={false}
          kindLabel={kindLabel}
          budgetUsage={hub.budgetUsage}
          showBudgetUsage={isEditing}
          showBudgetSection={
            permissions.scopes.includes("pay_invoice") ||
            permissions.scopes.includes("cash_redeem")
          }
          // A hub's budget looks like it should cap issuance and does not:
          // every mint funds its bill by internal transfer, which is exempt
          // from the budget cap (transactions_service.go's skipBudgetCap).
          // What it actually caps is an ordinary pay_invoice made over the
          // hub's own connection.
          budgetCaption={t("budget.cashHubCaption")}
        />
      </CardContent>
    </Card>
  );

  return (
    <div className="grid gap-5">
      <AppHeader
        title={
          <div className="flex flex-col sm:flex-row gap-2 sm:items-center">
            <div className="flex flex-row gap-2 items-center min-w-0">
              <AppAvatar app={hub} className="w-10 h-10 shrink-0" />
              <h2
                title={hub.name}
                className="min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap text-xl font-semibold"
              >
                {hub.name}
              </h2>
            </div>
            <Badge
              variant="positive"
              className="flex items-center gap-1 self-start sm:self-center"
            >
              {(siblingHubs?.length || 0) > 1 ? (
                <DropdownMenu modal={false} key={hub.id}>
                  <DropdownMenuTrigger>
                    <div className="flex items-center gap-1">
                      {appKindSiblingsLabel(
                        "cash_hub",
                        siblingHubs?.length ?? 0
                      )}{" "}
                      <ChevronDownIcon className="size-3 -mr-1" />
                    </div>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent className="w-56">
                    <DropdownMenuGroup>
                      {siblingHubs?.map((siblingHub) => (
                        <DropdownMenuItem key={siblingHub.id}>
                          <Link
                            to={`/cash-hub/${siblingHub.id}`}
                            className={cn(
                              "flex flex-1 items-center gap-2",
                              siblingHub.id === hub.id && "font-semibold"
                            )}
                          >
                            {siblingHub.name}
                          </Link>
                        </DropdownMenuItem>
                      ))}
                    </DropdownMenuGroup>
                  </DropdownMenuContent>
                </DropdownMenu>
              ) : (
                <>
                  <CheckCircleIcon className="w-3 h-3" />{" "}
                  {t("connections.connected", "Connected")}
                </>
              )}
            </Badge>
          </div>
        }
        description={tcircles("cashBills.dashboardSubtitle")}
        contentRight={
          isEditing ? (
            <div className="flex items-center gap-2">
              <Button type="button" variant="outline" onClick={cancelEditing}>
                {tc("actions.cancel", "Cancel")}
              </Button>
              <Button onClick={handleSave}>{tc("actions.save", "Save")}</Button>
            </div>
          ) : (
            <>
              <DropdownMenu modal={false}>
                <Button variant="outline" size="icon" asChild>
                  <DropdownMenuTrigger>
                    <EllipsisIcon />
                  </DropdownMenuTrigger>
                </Button>
                <DropdownMenuContent align="end">
                  <DropdownMenuGroup>
                    <DropdownMenuItem asChild>
                      {/* AppDetails' "Add Another Connection" points at the
                          generic /apps/new flow; for a Cash Hub the
                          equivalent is its own wizard. */}
                      <Link
                        to="/cash-hub/new"
                        className="flex flex-1 items-center gap-2"
                      >
                        <PlusIcon className="size-4" />{" "}
                        {tcircles("newCashHub.title")}
                      </Link>
                    </DropdownMenuItem>
                    <DropdownMenuItem asChild>
                      <div
                        className="flex items-center gap-2"
                        onClick={() => setShowConnectionDetails(true)}
                      >
                        <InfoIcon className="size-4" />{" "}
                        {t("connections.connectionDetails", "Connection Details")}
                      </div>
                    </DropdownMenuItem>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem variant="destructive" asChild>
                      <div
                        className="flex items-center gap-2"
                        onClick={() => setShowDeleteDialog(true)}
                      >
                        <Trash2Icon className="size-4" />{" "}
                        {t("connections.deleteKind", { kind: kindLabel })}
                      </div>
                    </DropdownMenuItem>
                  </DropdownMenuGroup>
                </DropdownMenuContent>
              </DropdownMenu>
              <ResponsiveButton
                variant="secondary"
                onClick={() => setIsEditing(true)}
                icon={SquarePenIcon}
                text={t("connections.editConnection")}
              />
            </>
          )
        }
      />

      {/* Page order is what an operator reaches for, in order: what the hub
          holds and owes, then the bills themselves plus the mint action,
          then the trends, then raw payments. The two charts are ~300px each
          and stack on a phone, so they sit behind a collapsed disclosure
          rather than pushing the list — the thing this page exists for —
          below the fold on every visit. */}
      {!isEditing && <CashHubOverview hub={hub} stats={stats} />}

      {isEditing && (
        <Card>
          <CardHeader>
            <CardTitle>{tc("labels.appName", "App Name")}</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex flex-row gap-2 items-center max-w-lg">
              <Input
                autoFocus
                type="text"
                name="name"
                value={name}
                id="name"
                onChange={(e) => setName(e.target.value)}
                required
                autoComplete="off"
              />
            </div>
          </CardContent>
        </Card>
      )}

      {isEditing && permissionsCard}

      {/* The hub's own expiry and its bill-expiry ceiling are set on the same
          screen but never checked against each other. Nothing is lost when
          they disagree — bills redeem against their own deadline on their own
          connection, and the sweep is a background job — but minting stops,
          which is worth knowing before saving. */}
      {isEditing && hubExpiresBeforeBills && (
        <Alert variant="warning">
          <TriangleAlertIcon className="h-4 w-4" />
          <AlertDescription>
            {t("circleHub.hubExpiryBeforeBillsWarning", { kind: kindLabel })}
          </AlertDescription>
        </Alert>
      )}

      {isEditing && (
        <CashHubConfigCard
          title={t("circleHub.hubSettingsTitle")}
          description={t("circleHub.cashHubSettingsDescription")}
          budgetLabel={t("circleHub.maxWalletBudgetLabel")}
          budgetHelper={t("circleHub.cashMaxWalletBudgetHelper")}
          expiryLabel={t("circleHub.maxWalletExpiryLabel")}
          expiryHelper={t("circleHub.cashMaxExpiryHelper")}
          perWalletMaxLoki={cashPerWalletMaxLoki}
          onPerWalletMaxLokiChange={setCashPerWalletMaxLoki}
          maxExpSecs={cashMaxExpSecs}
          onMaxExpSecsChange={setCashMaxExpSecs}
          minTransferLoki={cashMinTransferLoki}
          onMinTransferLokiChange={setCashMinTransferLoki}
          redeemFeePpm={cashRedeemFeePpm}
          onRedeemFeePpmChange={setCashRedeemFeePpm}
        />
      )}

      {!isEditing && (
        <>
          <Card>
            <CardHeader>
              <CardTitle>{t("circleHub.cashWalletsTitle")}</CardTitle>
              {!isCashFormOpen && (
                <CardAction>
                  <ResponsiveButton
                    size="sm"
                    onClick={() => allocationsRef.current?.openAdd()}
                    icon={PlusIcon}
                    text={t("circleHub.addCashWallet")}
                  />
                </CardAction>
              )}
            </CardHeader>
            <CardContent>
              <CashHubAllocations
                appId={hub.id}
                ref={allocationsRef}
                onFormOpenChange={setCashFormOpen}
              />
            </CardContent>
          </Card>

          {/* An expandable card, matching the cards around it — the charts
              inside render bare (see ChartFrame) so this is the only box. */}
          <Card>
            <Accordion
              type="single"
              collapsible
              value={isAnalyticsOpen ? "analytics" : ""}
              onValueChange={(v) => setAnalyticsOpen(v === "analytics")}
            >
              <AccordionItem value="analytics" className="border-b-0">
                {/* The checkbox is a sibling of the trigger, not a child:
                    the trigger is a <button>, and a second control nested
                    inside it could not be clicked without also toggling
                    the section. Shown only while open, since "keep open"
                    is a decision you make looking at the thing. */}
                <div className="flex items-center gap-3 px-6">
                  <AccordionTrigger className="flex-1 py-0 text-base font-semibold">
                    {t("circleHub.analyticsTitle")}
                  </AccordionTrigger>
                  {isAnalyticsOpen && (
                    <label className="text-muted-foreground flex shrink-0 cursor-pointer items-center gap-2 text-sm font-normal">
                      <Checkbox
                        checked={keepAnalyticsOpen}
                        onCheckedChange={(checked) =>
                          toggleKeepAnalyticsOpen(checked === true)
                        }
                      />
                      {t("circleHub.keepAnalyticsOpen")}
                    </label>
                  )}
                </div>
                <AccordionContent className="px-6 pt-4 pb-0">
                  <div className="grid grid-cols-1 items-start gap-5 lg:grid-cols-2">
                    <CashOutstandingChart
                    daily={stats.daily}
                    outstandingMloki={stats.outstanding_mloki}
                  />
                    <CashFlowChart daily={stats.daily} />
                  </div>
                </AccordionContent>
              </AccordionItem>
            </Accordion>
          </Card>

          <AppTransactionList appId={hub.id} />

          {permissionsCard}

          {showConnectionDetails && (
            <ConnectionDetailsModal
              app={hub}
              onClose={() => setShowConnectionDetails(false)}
            />
          )}
          {showDeleteDialog && (
            <DisconnectCashHub
              app={hub}
              onClose={() => setShowDeleteDialog(false)}
            />
          )}
        </>
      )}
    </div>
  );
}
