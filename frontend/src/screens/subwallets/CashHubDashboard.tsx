import { RegenerateCashHubConnection } from "src/components/listings/RegenerateCashHubConnection";
import {
  ChevronDownIcon,
  EllipsisIcon,
  InfoIcon,
  PlusIcon,
  RefreshCwIcon,
  SquarePenIcon,
  Trash2Icon,
  TriangleAlertIcon,
} from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { Link, Navigate, useParams } from "react-router";
import { toast } from "sonner";
import AppAvatar from "src/components/AppAvatar";
import AppHeader from "src/components/AppHeader";
import {
  CashCoverage,
  CashDailyFlow,
  CashExpiryRunway,
  CashOutstandingChart,
} from "src/components/cash/CashMonitorCharts";
import { CashHubOverview } from "src/components/cash/CashHubOverview";
import { CashHubWithdrawCard } from "src/components/withdraw/CashHubWithdrawCard";
import { CashHubPayoutsCard } from "src/components/withdraw/CashHubPayoutsCard";
import { CashHubOrderConnectionsCard } from "src/components/cash/CashHubOrderConnectionsCard";
import { CashHubOrderTermsCard } from "src/components/cash/CashHubOrderTermsCard";
import { CashHubOrdersCard } from "src/components/cash/CashHubOrdersCard";
import { CashHubConfigCard } from "src/components/CashHubConfigCard";
import { AppTransactionList } from "src/components/connections/AppTransactionList";
import { ConnectionDetailsModal } from "src/components/connections/ConnectionDetailsModal";
import { DisconnectCashHub } from "src/components/connections/DisconnectCashHub";
import Loading from "src/components/Loading";
import Permissions from "src/components/Permissions";
import BudgetExpiryFields from "src/components/BudgetExpiryFields";
import ResponsiveButton from "src/components/ResponsiveButton";
import { Alert, AlertDescription } from "src/components/ui/alert";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
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
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "src/components/ui/tabs";
import {
  DEFAULT_CASH_SPENT_RETENTION_SECS,
  SUBWALLET_APPSTORE_APP_ID,
} from "src/constants";
import { useApp } from "src/hooks/useApp";
import { useSiblingHubs } from "src/hooks/useApps";
import { useIsNarrow } from "src/hooks/useIsNarrow";
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
  const { t: tlistings } = useTranslation("listings");
  // On a phone the overview's flow stats move into a Summary tab and the tab
  // bar sticks, so the page reads as focused screens rather than one long
  // scroll re-shown under every tab. Desktop keeps the single-card layout.
  const isMobile = useIsNarrow();

  const allocationsRef = React.useRef<CashHubAllocationsHandle>(null);
  // Only the setter is used now (synced to CashHubAllocations via
  // onFormOpenChange); the Mint button lives inside the list's header row.
  const [, setCashFormOpen] = React.useState(false);
  // Controlled so a phone can open on the Summary tab (the approved mobile
  // layout) while desktop still opens on the bills list. Lazy-initialised from
  // the real viewport width so the choice is right on first paint, not after
  // the media-query hook resolves a tick later.
  const [activeTab, setActiveTab] = React.useState<string>(() =>
    typeof window !== "undefined" && window.innerWidth < 768
      ? "summary"
      : "wallets"
  );
  const [isEditing, setIsEditing] = React.useState(false);
  const [showConnectionDetails, setShowConnectionDetails] =
    React.useState(false);
  const [showDeleteDialog, setShowDeleteDialog] = React.useState(false);
  const [showRegenerateDialog, setShowRegenerateDialog] = React.useState(false);
  // The Summary tab exists only on a phone. If this is desktop (or the viewport
  // grows to it while Summary is active), fall back to the bills tab at render
  // time — deriving rather than resetting state avoids a first-paint race with
  // the viewport hook.
  const effectiveTab =
    !isMobile && activeTab === "summary" ? "wallets" : activeTab;

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
  // ?? the default rather than 0: a hub created before this field existed
  // reports it absent, and 0 would silently mean "disabled" instead.
  const [cashSpentRetentionSecs, setCashSpentRetentionSecs] = React.useState(
    hub.cashSpentRetentionSecs ?? DEFAULT_CASH_SPENT_RETENTION_SECS
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
        cashSpentRetentionSecs,
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
      toast(
        t("connections.successfullyUpdated", "Successfully updated connection")
      );
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
    setCashSpentRetentionSecs(
      hub.cashSpentRetentionSecs ?? DEFAULT_CASH_SPENT_RETENTION_SECS
    );
    setIsEditing(false);
  };

  // Rendered in both modes but in different places: while editing it is one
  // of the three things being edited, so it sits with them near the top;
  // while reading it is settled configuration, so it sits below the bills,
  // the trends and the payments an operator actually came here for.
  // The Analytics tab's content: the same four readings as the Cash Hubs
  // list, scoped to this hub — can it honour what it owes, what happens to
  // that next, how it got here, and what moved each day. Lives in its own
  // tab now (discoverable, full-width) rather than buried in a collapsible
  // at the foot of the Overview card. The balance comes off the hub's own
  // App row; a single hub's stats response does not carry one.
  const analyticsPanel = (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      {/* A card that is a few figures rather than a plot takes the whole row
          instead of sitting half-empty beside one, and the two time series
          go wide, where a 30-day axis is readable. */}
      <CashCoverage
        className="lg:col-span-2"
        backingMloki={stats.backing_mloki}
        outstandingMloki={stats.outstanding_mloki}
        shortfallMloki={stats.shortfall_mloki}
        capacityMloki={hub.balance}
      />
      <CashExpiryRunway className="lg:col-span-2" stats={stats} />
      <CashOutstandingChart
        daily={stats.daily}
        outstandingMloki={stats.outstanding_mloki}
      />
      <CashDailyFlow daily={stats.daily} />
    </div>
  );

  // Two groups: Permissions (scopes) + a separate Budget & expiry, so expiry
  // and renewal don't read as "permissions". Used in both the tab and the edit
  // form.
  const permissionsCard = (
    <div className="grid gap-4">
      <Card>
        {isEditing && (
          <CardHeader>
            <CardTitle>{t("permissions.title", "Permissions")}</CardTitle>
          </CardHeader>
        )}
        <CardContent>
          <Permissions
            capabilities={capabilities}
            permissions={isEditing ? permissions : savedPermissions}
            setPermissions={setPermissions}
            readOnly={!isEditing}
            scopesReadOnly
            isNewConnection={false}
            kindLabel={kindLabel}
            showBudgetAndExpiry={false}
          />
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>{t("circleHub.budgetExpiryTitle")}</CardTitle>
          {isEditing && (
            <CardDescription>
              {t("circleHub.budgetManagedNote")}
            </CardDescription>
          )}
        </CardHeader>
        <CardContent>
          <BudgetExpiryFields
            permissions={isEditing ? permissions : savedPermissions}
            setPermissions={setPermissions}
            readOnly={!isEditing}
            isNewConnection={false}
            budgetUsage={hub.budgetUsage}
            showBudgetSection={
              permissions.scopes.includes("pay_invoice") ||
              permissions.scopes.includes("cash_redeem")
            }
            // A hub's budget looks like it should cap issuance and does not:
            // every mint funds its bill by internal transfer, which is exempt
            // from the budget cap. What it caps is an ordinary pay_invoice made
            // over the hub's own connection.
            budgetCaption={t("budget.cashHubCaption")}
          />
        </CardContent>
      </Card>
    </div>
  );

  // grid-cols-1 (= minmax(0,1fr)) clamps this single column to the available
  // width. A bare `grid` is an auto track that instead grows to its widest
  // child's min-content — here the 6-tab bill filter — which would widen the
  // page and give it a horizontal scrollbar on narrow screens instead of
  // letting the tab strip scroll inside its own box.
  return (
    <div className="grid grid-cols-1 gap-5">
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
                  {/* A live-status dot + "Live" (the redesign's header chip),
                      not a generic "Connected" — the hub is active and able to
                      honour its bills. */}
                  <span className="size-2 rounded-full bg-current" />
                  {t("circleHub.liveChip", "Live")}
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
                    {/* Phone: the header keeps a single action button, so Edit
                        lives in this menu instead of beside it. */}
                    {isMobile && (
                      <>
                        <DropdownMenuItem asChild>
                          <div
                            className="flex items-center gap-2"
                            onClick={() => setIsEditing(true)}
                          >
                            <SquarePenIcon className="size-4" />{" "}
                            {tc("actions.edit", "Edit")}
                          </div>
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                      </>
                    )}
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
                        {t(
                          "connections.connectionDetails",
                          "Connection Details"
                        )}
                      </div>
                    </DropdownMenuItem>
                    <DropdownMenuItem asChild>
                      <div
                        className="flex items-center gap-2"
                        onClick={() => setShowRegenerateDialog(true)}
                      >
                        <RefreshCwIcon className="size-4" />{" "}
                        {tlistings("regenerate.menuItem")}
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
              {!isMobile && (
                <ResponsiveButton
                  variant="secondary"
                  onClick={() => setIsEditing(true)}
                  icon={SquarePenIcon}
                  text={tc("actions.edit", "Edit")}
                />
              )}
            </>
          )
        }
      />
      <RegenerateCashHubConnection
        hubId={hub.id}
        open={showRegenerateDialog}
        onOpenChange={setShowRegenerateDialog}
      />

      {/* Page order is what an operator reaches for, in order: what the hub
          holds and owes (Overview), then the bills, payments and charts as
          tabs. Analytics is its own tab rather than a collapsible buried at
          the foot of the Overview card. */}
      {!isEditing && (
        <div className={isMobile ? "pb-1" : "border-b pb-5"}>
          {/* Phone: only the hero (balance + controls + coverage) stays here;
              the flow stats live in the Summary tab below. Desktop keeps the
              whole overview above the tabs. */}
          <CashHubOverview
            hub={hub}
            stats={stats}
            section={isMobile ? "hero" : "full"}
          />
        </div>
      )}

      {isEditing && (
        // The hub settings form is a single column of fields; cap it so inputs
        // don't sprawl the full width of a desktop. Left-aligned to sit under
        // the page header rather than floating centre.
        <div className="grid w-full max-w-2xl gap-5">
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

          {permissionsCard}

          {/* The hub's own expiry and its bill-expiry ceiling are set on the same
          screen but never checked against each other. Nothing is lost when
          they disagree — bills redeem against their own deadline on their own
          connection, and the sweep is a background job — but minting stops,
          which is worth knowing before saving. */}
          {hubExpiresBeforeBills && (
            <Alert variant="warning">
              <TriangleAlertIcon className="h-4 w-4" />
              <AlertDescription>
                {t("circleHub.hubExpiryBeforeBillsWarning", {
                  kind: kindLabel,
                })}
              </AlertDescription>
            </Alert>
          )}

          <CashHubConfigCard
            title={t("circleHub.hubSettingsTitle")}
            description={t("circleHub.cashHubSettingsDescription")}
            budgetLabel={t("circleHub.cashMaxBudgetLabel")}
            budgetHelper={t("circleHub.cashMaxWalletBudgetHelper")}
            expiryLabel={t("circleHub.cashMaxExpiryLabel")}
            expiryHelper={t("circleHub.cashMaxExpiryHelper")}
            perWalletMaxLoki={cashPerWalletMaxLoki}
            onPerWalletMaxLokiChange={setCashPerWalletMaxLoki}
            maxExpSecs={cashMaxExpSecs}
            onMaxExpSecsChange={setCashMaxExpSecs}
            minTransferLoki={cashMinTransferLoki}
            onMinTransferLokiChange={setCashMinTransferLoki}
            redeemFeePpm={cashRedeemFeePpm}
            onRedeemFeePpmChange={setCashRedeemFeePpm}
            spentRetentionSecs={cashSpentRetentionSecs}
            onSpentRetentionSecsChange={setCashSpentRetentionSecs}
          />
        </div>
      )}

      {!isEditing && (
        <>
          <Tabs
            value={effectiveTab}
            onValueChange={setActiveTab}
            className="w-full"
          >
            {/* On a phone the tab bar sticks to the top (full-bleed via -mx-4)
                so switching tabs never scrolls the hero back into view; on
                desktop it's an ordinary inline row. */}
            <div
              className={cn(
                "bg-background",
                isMobile && "sticky top-0 z-20 -mx-4 border-b px-4 pb-2"
              )}
            >
              <TabsList variant="underline">
                {isMobile && (
                  <TabsTrigger value="summary">
                    {t("tabs.summary", "Summary")}
                  </TabsTrigger>
                )}
                <TabsTrigger value="wallets">
                  {t("circleHub.cashWalletsTitle")}
                </TabsTrigger>
                <TabsTrigger value="analytics">
                  {t("circleHub.analyticsTitle")}
                </TabsTrigger>
                <TabsTrigger value="activity">
                  {t("tabs.activity", "Activity")}
                </TabsTrigger>
                <TabsTrigger value="permissions">
                  {t("permissions.title", "Permissions")}
                </TabsTrigger>
                {/* Selling/payout tools used to live only on the legacy
                    /apps/:id route, out of reach for managed hubs. They come
                    home here so one hub's whole lifecycle is in one place. */}
                <TabsTrigger value="sell">
                  {t("circleHub.sellRedeemTab", "Sell & redeem")}
                </TabsTrigger>
              </TabsList>
            </div>
            {isMobile && (
              <TabsContent value="summary">
                {/* The flow stats + median + budget, split out of the hero
                    above so the other tabs aren't buried under them. */}
                <CashHubOverview hub={hub} stats={stats} section="details" />
              </TabsContent>
            )}
            <TabsContent value="wallets">
              {/* No heading — the active tab already reads "Cash Bills". The
                  Mint button rides on the list's filter-tabs row (headerAction)
                  so it lines up with the filters instead of floating above. */}
              <CashHubAllocations
                appId={hub.id}
                ref={allocationsRef}
                onFormOpenChange={setCashFormOpen}
                headerAction={
                  <ResponsiveButton
                    size="sm"
                    onClick={() => allocationsRef.current?.openAdd()}
                    icon={PlusIcon}
                    text={t("circleHub.addCashWallet")}
                  />
                }
              />
            </TabsContent>
            <TabsContent value="analytics">{analyticsPanel}</TabsContent>
            <TabsContent value="activity">
              <AppTransactionList appId={hub.id} />
            </TabsContent>
            <TabsContent value="permissions">{permissionsCard}</TabsContent>
            <TabsContent value="sell" className="grid gap-4">
              <CashHubWithdrawCard hubId={hub.id} />
              <CashHubPayoutsCard hubId={hub.id} />
              <CashHubOrderConnectionsCard hubId={hub.id} />
              <CashHubOrderTermsCard hubId={hub.id} />
              <CashHubOrdersCard hubId={hub.id} />
            </TabsContent>
          </Tabs>

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
