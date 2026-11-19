import {
  CheckCircleIcon,
  ChevronDownIcon,
  EllipsisIcon,
  InfoIcon,
  PlusIcon,
  SquarePenIcon,
  Trash2Icon,
} from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { toast } from "sonner";
import AppAvatar from "src/components/AppAvatar";
import AppHeader from "src/components/AppHeader";
import BudgetRenewalSelect from "src/components/BudgetRenewalSelect";
import { CircleAnalytics } from "src/components/circles/CircleAnalytics";
import {
  CircleAcceptingCard,
  CircleMemberRequestCard,
} from "src/components/circles/CircleHubControls";
import { CircleHubOverview } from "src/components/circles/CircleHubOverview";
import {
  CircleHubSettings,
  CircleHubSettingsFields,
} from "src/components/circles/CircleHubSettingsFields";
import { CircleIdentityCard } from "src/components/circles/CircleIdentityCard";
import { AppTransactionList } from "src/components/connections/AppTransactionList";
import { ConnectionDetailsModal } from "src/components/connections/ConnectionDetailsModal";
import { DisconnectCircleHub } from "src/components/connections/DisconnectCircleHub";
import { CircleListingPanel } from "src/components/listings/CircleListingPanel";
import { CurrencyInput } from "src/components/CurrencyInput";
import { DurationInput } from "src/components/DurationInput";
import { OverviewCardSkeleton } from "src/components/OverviewCardSkeleton";
import Permissions from "src/components/Permissions";
import ResponsiveButton from "src/components/ResponsiveButton";
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
import { Label } from "src/components/ui/label";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "src/components/ui/tabs";
import {
  LOKI_ACCOUNT_APP_NAME,
  MIN_CIRCLE_WALLET_MAX_LOKI,
  SUBWALLET_APPSTORE_APP_ID,
  WEEK_SCALE_PRESETS,
} from "src/constants";
import { useSiblingHubs } from "src/hooks/useApps";
import { useCircleHubStats } from "src/hooks/useCircleHubStats";
import { useIsNarrow } from "src/hooks/useIsNarrow";
import { useInputUnit, useUnit } from "src/hooks/useUnit";
import { cn } from "src/lib/utils";
import {
  CircleAllowlist,
  CircleAllowlistHandle,
} from "src/screens/subwallets/CircleAllowlist";
import { CircleWallets } from "src/screens/subwallets/CircleWallets";
import {
  App,
  AppPermissions,
  BudgetRenewalType,
  UpdateAppRequest,
  WalletCapabilities,
} from "src/types";
import { appKindLabel, appKindSiblingsLabel } from "src/utils/appKind";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

type CircleHubDashboardProps = {
  app: App;
  capabilities: WalletCapabilities;
  refetchApp: () => void;
};

// A circle hub's own detail page, split out of the shared AppDetails (as cash
// got CashHubDashboard) so it can present cleanly — the overview and circle
// controls up top, then Allowlist / Wallets / Activity / Permissions in tabs
// instead of one long scroll. AppDetails delegates here for kind === circle_hub.
export function CircleHubDashboard({
  app,
  capabilities,
  refetchApp,
}: CircleHubDashboardProps) {
  return (
    <CircleHubDashboardInternal
      key={app.id}
      app={app}
      capabilities={capabilities}
      refetchApp={refetchApp}
    />
  );
}

function CircleHubDashboardInternal({
  app,
  capabilities,
  refetchApp,
}: CircleHubDashboardProps) {
  const { t } = useTranslation("apps");
  const { t: tc } = useTranslation("common");
  const { t: tCircles } = useTranslation("circles");
  const { data: stats } = useCircleHubStats(app.id);
  const { scaleInputAmount, parseInputAmount } = useUnit();
  // Phone layout: the balance/allocated lead stays pinned, the rest of the
  // overview moves into a Summary tab, and the tab bar sticks. Desktop keeps
  // the single-card layout. See CashHubDashboard.
  const isMobile = useIsNarrow();

  const circleAllowlistRef = React.useRef<CircleAllowlistHandle>(null);
  const [isAllowlistFormOpen, setAllowlistFormOpen] = React.useState(false);
  // Controlled so a phone opens on the Summary tab while desktop opens on the
  // wallets list. Lazy-initialised from the real viewport width so it's right
  // on first paint.
  const [activeTab, setActiveTab] = React.useState<string>(() =>
    typeof window !== "undefined" && window.innerWidth < 768
      ? "summary"
      : "wallets"
  );
  // The Summary tab exists only on a phone; derive a safe tab at render time so
  // desktop (or a resize to it) never lands on a tab that isn't there.
  const effectiveTab =
    !isMobile && activeTab === "summary" ? "wallets" : activeTab;
  const [isEditing, setIsEditing] = React.useState(false);
  const [showConnectionDetails, setShowConnectionDetails] =
    React.useState(false);
  const [showDeleteDialog, setShowDeleteDialog] = React.useState(false);

  const [name, setName] = React.useState(app.name);
  const [permissions, setPermissions] = React.useState<AppPermissions>({
    scopes: app.scopes,
    maxAmount: app.maxAmount,
    budgetRenewal: app.budgetRenewal,
    expiresAt: app.expiresAt ? new Date(app.expiresAt) : undefined,
    isolated: app.isolated,
  });
  const [savedPermissions, setSavedPermissions] =
    React.useState<AppPermissions>(permissions);

  const [circleMaxExpSecs, setCircleMaxExpSecs] = React.useState(
    app.circleMaxExpSecs ?? 0
  );
  const [circleFeesPpm, setCircleFeesPpm] = React.useState(
    app.circleFeesPpm ?? 0
  );
  const [circlePerWalletMaxLoki, setCirclePerWalletMaxLoki] = React.useState(
    app.circlePerWalletMaxMloki ? app.circlePerWalletMaxMloki / 1000 : 0
  );
  const [circleMinBudgetRenewal, setCircleMinBudgetRenewal] =
    React.useState<BudgetRenewalType>(app.circleMinBudgetRenewal ?? "monthly");
  const [circleSettings, setCircleSettings] = React.useState<CircleHubSettings>(
    () => ({
      circleDefaultExpSecs: app.circleDefaultExpSecs ?? 0,
      circlePerWalletMaxReceiveMloki: app.circlePerWalletMaxReceiveMloki ?? 0,
      circleHoldingsCeilingMloki: app.circleHoldingsCeilingMloki ?? 0,
      circleFeeReservePpm: app.circleFeeReservePpm ?? 0,
      circleFeeReserveMinMloki: app.circleFeeReserveMinMloki ?? 0,
      circleOutboundCeilingMloki: app.circleOutboundCeilingMloki ?? 0,
      circleOutboundPeriod: app.circleOutboundPeriod ?? "monthly",
      circleGraceSecs: app.circleGraceSecs ?? 0,
      circleDustThresholdMloki: app.circleDustThresholdMloki ?? 0,
      circleRotationEnabled: app.circleRotationEnabled ?? false,
      circleRotationDelaySecs: app.circleRotationDelaySecs ?? 0,
    })
  );
  const [inputUnit, setInputUnit] = useInputUnit(circlePerWalletMaxLoki);

  const kindLabel = appKindLabel("circle_hub");
  const isManagedHub =
    app.metadata?.app_store_app_id === SUBWALLET_APPSTORE_APP_ID;
  const siblingHubs = useSiblingHubs(isManagedHub ? "circle_hub" : undefined);

  const handleSave = async () => {
    try {
      const updateAppRequest: UpdateAppRequest = {
        name,
        budgetRenewal: permissions.budgetRenewal,
        expiresAt: permissions.expiresAt?.toISOString(),
        updateExpiresAt: true,
        maxAmount: permissions.maxAmount,
        circleMaxExpSecs,
        circleFeesPpm,
        circlePerWalletMaxMloki: circlePerWalletMaxLoki * 1000,
        circleMinBudgetRenewal,
        ...circleSettings,
      };

      await request(`/api/apps/${app.id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(updateAppRequest),
      });

      refetchApp();
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
    setName(app.name);
    setPermissions(savedPermissions);
    setCircleMaxExpSecs(app.circleMaxExpSecs ?? 0);
    setCircleFeesPpm(app.circleFeesPpm ?? 0);
    setCirclePerWalletMaxLoki(
      app.circlePerWalletMaxMloki ? app.circlePerWalletMaxMloki / 1000 : 0
    );
    setCircleMinBudgetRenewal(app.circleMinBudgetRenewal ?? "monthly");
    setCircleSettings({
      circleDefaultExpSecs: app.circleDefaultExpSecs ?? 0,
      circlePerWalletMaxReceiveMloki: app.circlePerWalletMaxReceiveMloki ?? 0,
      circleHoldingsCeilingMloki: app.circleHoldingsCeilingMloki ?? 0,
      circleFeeReservePpm: app.circleFeeReservePpm ?? 0,
      circleFeeReserveMinMloki: app.circleFeeReserveMinMloki ?? 0,
      circleOutboundCeilingMloki: app.circleOutboundCeilingMloki ?? 0,
      circleOutboundPeriod: app.circleOutboundPeriod ?? "monthly",
      circleGraceSecs: app.circleGraceSecs ?? 0,
      circleDustThresholdMloki: app.circleDustThresholdMloki ?? 0,
      circleRotationEnabled: app.circleRotationEnabled ?? false,
      circleRotationDelaySecs: app.circleRotationDelaySecs ?? 0,
    });
    setIsEditing(false);
  };

  const permissionsCard = (
    <Card>
      {/* Title only in edit mode — in the Permissions tab it would just repeat
          the active tab's label. */}
      {isEditing && (
        <CardHeader>
          <CardTitle>{t("permissions.title", "Permissions")}</CardTitle>
        </CardHeader>
      )}
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
          budgetUsage={app.budgetUsage}
          showBudgetUsage={isEditing}
          showBudgetSection
          budgetCaption={t(
            "budget.circleSharedCaption",
            "Shared by all wallets in this circle."
          )}
        />
      </CardContent>
    </Card>
  );

  const isAllowlistPolicy = app.circleIdentity?.policy === "allowlist";

  // grid-cols-1 (= minmax(0,1fr)) clamps the single column to the page width
  // so a wide child (filter tabs, long rows) scrolls inside its own box
  // instead of widening the page — see CashHubDashboard.
  return (
    <div className="grid grid-cols-1 gap-5">
      <AppHeader
        title={
          <div className="flex flex-col sm:flex-row gap-2 sm:items-center">
            <div className="flex flex-row gap-2 items-center min-w-0">
              <AppAvatar app={app} className="w-10 h-10 shrink-0" />
              <h2
                title={app.name}
                className="min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap text-xl font-semibold"
              >
                {app.name}
              </h2>
            </div>
            <Badge
              variant="positive"
              className="flex items-center gap-1 self-start sm:self-center"
            >
              {(siblingHubs?.length || 0) > 1 ? (
                <DropdownMenu modal={false} key={app.id}>
                  <DropdownMenuTrigger>
                    <div className="flex items-center gap-1">
                      {appKindSiblingsLabel(
                        "circle_hub",
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
                            to={`/apps/${siblingHub.id}`}
                            className={cn(
                              "flex flex-1 items-center gap-2",
                              siblingHub.id === app.id && "font-semibold"
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
        description={tCircles(
          "circleHub.dashboardSubtitle",
          "Wallets you host for members of this circle, and how they're used"
        )}
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
                            {t("connections.editConnection")}
                          </div>
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                      </>
                    )}
                    <DropdownMenuItem asChild>
                      <Link
                        to="/sub-wallets/new/circle"
                        className="flex flex-1 items-center gap-2"
                      >
                        <PlusIcon className="size-4" />{" "}
                        {tCircles("newCircleHub.title")}
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
                  text={t("connections.editConnection")}
                />
              )}
            </>
          )
        }
      />

      {!isEditing && (
        <>
          {/* Lead with the Overview (the money), then everything else as
              tabs — matching the Cash Hub detail. The circle's operator
              controls (identity, accepting new members, member requests,
              listing) used to stack as four cards above the tabs, pushing
              the figures down the page; they now live together in a
              Settings tab. */}
          <div className={isMobile ? "pb-1" : "border-b pb-5"}>
            {/* Phone: only the balance/allocated lead stays here; the rest of
                the overview lives in the Summary tab below. */}
            {stats ? (
              <CircleHubOverview
                hub={app}
                stats={stats}
                section={isMobile ? "hero" : "full"}
              />
            ) : (
              <OverviewCardSkeleton />
            )}
          </div>

          {/* The circle's public-listing control, right under the circle —
              list / unlist / manage without leaving this page. */}
          <CircleListingPanel appId={app.id} />

          <Tabs
            value={effectiveTab}
            onValueChange={setActiveTab}
            className="w-full"
          >
            {/* Phone: the tab bar sticks (full-bleed via -mx-4) so switching
                tabs never scrolls the hero back; desktop is an inline row. */}
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
                {isAllowlistPolicy && (
                  <TabsTrigger value="allowlist">
                    {t("circleHub.allowlistTitle")}
                  </TabsTrigger>
                )}
                <TabsTrigger value="wallets">
                  {t("circleHub.circleWalletsTitle")}
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
                <TabsTrigger value="settings">
                  {t("circleHub.settingsTab")}
                </TabsTrigger>
              </TabsList>
            </div>

            {isMobile && stats && (
              <TabsContent value="summary">
                {/* The spent/received/fees/members grid, split out of the hero
                    above so the other tabs aren't buried under it. */}
                <CircleHubOverview hub={app} stats={stats} section="details" />
              </TabsContent>
            )}

            {isAllowlistPolicy && (
              <TabsContent value="allowlist">
                {/* No heading — the active tab already reads "Allowlist". */}
                {!isAllowlistFormOpen && (
                  <div className="mb-3 flex justify-end">
                    <ResponsiveButton
                      size="sm"
                      onClick={() => circleAllowlistRef.current?.openAdd()}
                      icon={PlusIcon}
                      text={t("circleHub.addMember")}
                    />
                  </div>
                )}
                <CircleAllowlist
                  appId={app.id}
                  ref={circleAllowlistRef}
                  onFormOpenChange={setAllowlistFormOpen}
                />
              </TabsContent>
            )}

            <TabsContent value="wallets">
              {/* No heading — the active tab already reads "Circle Wallets". */}
              <CircleWallets appId={app.id} />
            </TabsContent>

            <TabsContent value="analytics">
              {stats ? (
                <CircleAnalytics hub={app} stats={stats} />
              ) : (
                <OverviewCardSkeleton />
              )}
            </TabsContent>

            <TabsContent value="activity">
              <AppTransactionList appId={app.id} />
            </TabsContent>

            <TabsContent value="permissions">{permissionsCard}</TabsContent>

            <TabsContent value="settings">
              <div className="grid gap-4">
                {app.circleIdentity && (
                  <CircleIdentityCard
                    appId={app.id}
                    identity={app.circleIdentity}
                  />
                )}
                <CircleAcceptingCard app={app} onChanged={refetchApp} />
                <CircleMemberRequestCard app={app} />
              </div>
            </TabsContent>
          </Tabs>

          {showConnectionDetails && (
            <ConnectionDetailsModal
              app={app}
              onClose={() => setShowConnectionDetails(false)}
            />
          )}
          {showDeleteDialog && (
            <DisconnectCircleHub
              app={app}
              onClose={() => setShowDeleteDialog(false)}
            />
          )}
        </>
      )}

      {isEditing && (
        // Single-column settings form — capped so inputs don't sprawl the
        // full desktop width, left-aligned under the page header.
        <div className="grid w-full max-w-2xl gap-5">
          {app.name !== LOKI_ACCOUNT_APP_NAME && (
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

          {permissionsCard}

          <Card>
            <CardHeader>
              <CardTitle>{t("circleHub.hubSettingsTitle")}</CardTitle>
              <CardDescription>
                {t("circleHub.circleHubSettingsDescription")}
              </CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4 max-w-lg">
              <div className="grid gap-1.5">
                <Label htmlFor="circlePerWalletMax">
                  {t("circleHub.maxWalletBudgetLabel")}
                </Label>
                <CurrencyInput
                  id="circlePerWalletMax"
                  amount={
                    circlePerWalletMaxLoki
                      ? scaleInputAmount(
                          circlePerWalletMaxLoki,
                          inputUnit
                        ).toString()
                      : ""
                  }
                  onAmountChange={(val) =>
                    setCirclePerWalletMaxLoki(
                      parseInputAmount(parseFloat(val) || 0, inputUnit)
                    )
                  }
                  inputUnit={inputUnit}
                  onInputUnitChange={setInputUnit}
                  required
                  min={scaleInputAmount(MIN_CIRCLE_WALLET_MAX_LOKI, inputUnit)}
                />
                <p className="text-muted-foreground text-sm">
                  {tCircles("newCircleHub.minWalletBudgetHint", {
                    min: MIN_CIRCLE_WALLET_MAX_LOKI,
                  })}
                </p>
              </div>

              <div className="grid gap-1.5">
                <BudgetRenewalSelect
                  label={t("circleHub.minRenewalLabel")}
                  value={circleMinBudgetRenewal}
                  onChange={setCircleMinBudgetRenewal}
                />
              </div>

              <div className="grid gap-1.5">
                <Label htmlFor="circleMaxExpSecs">
                  {t("circleHub.maxWalletExpiryLabel")}
                </Label>
                <DurationInput
                  id="circleMaxExpSecs"
                  seconds={circleMaxExpSecs}
                  onChange={setCircleMaxExpSecs}
                  min={60}
                  presets={WEEK_SCALE_PRESETS}
                />
                <p className="text-muted-foreground text-sm">
                  {t("circleHub.circleMaxExpiryHelper")}
                </p>
              </div>

              <div className="grid gap-1.5">
                <Label htmlFor="circleFeesPpm">
                  {t("circleHub.feePpmLabel")}
                </Label>
                <Input
                  id="circleFeesPpm"
                  type="number"
                  min={0}
                  value={circleFeesPpm}
                  onChange={(e) => setCircleFeesPpm(Number(e.target.value))}
                />
                <p className="text-muted-foreground text-sm">
                  {t("circleHub.feePpmHelper")}
                </p>
              </div>

              <CircleHubSettingsFields
                value={circleSettings}
                onChange={setCircleSettings}
              />
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  );
}
