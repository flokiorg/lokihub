import React from "react";
import { Link, useLocation, useParams } from "react-router-dom";

import {
  App,
  AppPermissions,
  BudgetRenewalType,
  UpdateAppRequest,
  WalletCapabilities,
} from "src/types";

import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request"; // build the project for this to appear

import {
  CheckCircleIcon,
  ChevronDownIcon,
  EllipsisIcon,
  InfoIcon,
  PlusIcon,
  QrCodeIcon,
  SquarePenIcon,
  UnplugIcon,
} from "lucide-react";
import { toast } from "sonner";
import AppAvatar from "src/components/AppAvatar";
import AppHeader from "src/components/AppHeader";
import BudgetRenewalSelect from "src/components/BudgetRenewalSelect";
import { AboutAppCard } from "src/components/connections/AboutAppCard";
import { AppLinksCard } from "src/components/connections/AppLinksCard";
import { AppTransactionList } from "src/components/connections/AppTransactionList";
import { AppUsage } from "src/components/connections/AppUsage";
import {
  CircleAllowlist,
  CircleAllowlistHandle,
} from "src/screens/subwallets/CircleAllowlist";
import { CircleWallets } from "src/screens/subwallets/CircleWallets";
import {
  JITHubAllocations,
  JITHubAllocationsHandle,
} from "src/screens/subwallets/JITHubAllocations";
import { ChildIdentityCard } from "src/components/circles/ChildIdentityCard";
import { CircleIdentityCard } from "src/components/circles/CircleIdentityCard";
import { ConnectionDetailsModal } from "src/components/connections/ConnectionDetailsModal";
import { RevealConnectionDialog } from "src/components/connections/RevealConnectionDialog";
import { CurrencyInput } from "src/components/CurrencyInput";
import { DisconnectApp } from "src/components/connections/DisconnectApp";
import { DisconnectCircleHub } from "src/components/connections/DisconnectCircleHub";
import { DisconnectJITHub } from "src/components/connections/DisconnectJITHub";
import { DurationInput } from "src/components/DurationInput";
import { JITHubConfigCard } from "src/components/JITHubConfigCard";
import { AppStoreApp } from "src/components/connections/SuggestedAppData";
import { useAppStore } from "src/hooks/useAppStore";
import Loading from "src/components/Loading";
import Permissions from "src/components/Permissions";
import ResponsiveButton from "src/components/ResponsiveButton";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "src/components/ui/alert-dialog";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardAction,
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
import { LOKI_ACCOUNT_APP_NAME, WEEK_SCALE_PRESETS } from "src/constants";
import { useApp } from "src/hooks/useApp";
import { useAppsForAppStoreApp } from "src/hooks/useApps";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { primaryProfileLabel } from "src/utils/nostrProfileLabel";
import { useCapabilities } from "src/hooks/useCapabilities";
import { useInputUnit, useUnit } from "src/hooks/useUnit";
import { cn } from "src/lib/utils";
import { useTranslation } from "react-i18next";
import { Trans } from "react-i18next";

function AppDetails() {
  const { id } = useParams() as { id: string };
  const { data: app, mutate: refetchApp, error } = useApp(parseInt(id));
  const { data: capabilities } = useCapabilities();

  if (error) {
    return <p className="text-red-500">{error.message}</p>;
  }

  if (!app || !capabilities) {
    return <Loading />;
  }

  return (
    <AppInternal
      key={app.id}
      app={app}
      refetchApp={refetchApp}
      capabilities={capabilities}
    />
  );
}

type AppInternalProps = {
  app: App;
  capabilities: WalletCapabilities;
  refetchApp: () => void;
};

function AppInternal({ app, refetchApp, capabilities }: AppInternalProps) {
  const location = useLocation();
  const circleAllowlistRef = React.useRef<CircleAllowlistHandle>(null);
  const jitHubAllocationsRef = React.useRef<JITHubAllocationsHandle>(null);
  const [isJitFormOpen, setJitFormOpen] = React.useState(false);
  const [isAllowlistFormOpen, setAllowlistFormOpen] = React.useState(false);
  const [isEditingPermissions, setIsEditingPermissions] = React.useState(false);
  const [showConnectionDetails, setShowConnectionDetails] =
    React.useState(false);
  const [showDisconnectAppDialog, setShowDisconnectAppDialog] =
    React.useState(false);
  // A JIT wallet's pairing key is deterministically re-derivable server-side
  // (unlike a normal app's, which is random and discarded after creation), so
  // its connection string can be shown again at any time — see
  // GetJITWalletConnection on the backend.
  const [jitConnectionUri, setJitConnectionUri] = React.useState<
    string | undefined
  >(undefined);
  const [isLoadingJitConnection, setLoadingJitConnection] =
    React.useState(false);

  const handleShowJitConnection = async () => {
    setLoadingJitConnection(true);
    try {
      const result = await request<{ pairing_uri: string }>(
        `/api/apps/${app.id}/jit-connection`
      );
      if (result) {
        setJitConnectionUri(result.pairing_uri);
      }
    } catch (error) {
      handleRequestError(t("circleHub.errors.loadJitConnection"), error);
    }
    setLoadingJitConnection(false);
  };
  const { t } = useTranslation("apps");
  const { t: tc } = useTranslation("common");

  React.useEffect(() => {
    const queryParams = new URLSearchParams(location.search);
    const editMode = queryParams.has("edit");
    setIsEditingPermissions(editMode);
  }, [location.search]);

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

  // Hub-level defaults (jit_hub, circle_hub) — set at creation time but
  // otherwise not exposed anywhere, so Edit Connection is the only place to
  // change them after the fact.
  const { scaleInputAmount, parseInputAmount } = useUnit();
  const hubPerWalletMaxLoki =
    app.kind === "jit_hub"
      ? app.jitPerWalletMaxMloki
        ? app.jitPerWalletMaxMloki / 1000
        : 0
      : app.circlePerWalletMaxMloki
        ? app.circlePerWalletMaxMloki / 1000
        : 0;
  const [inputUnit, setInputUnit] = useInputUnit(hubPerWalletMaxLoki);
  const [jitPerWalletMaxLoki, setJitPerWalletMaxLoki] = React.useState(
    app.jitPerWalletMaxMloki ? app.jitPerWalletMaxMloki / 1000 : 0
  );
  const [jitMaxExpSecs, setJitMaxExpSecs] = React.useState(
    app.jitMaxExpSecs ?? 0
  );
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

  // These kinds are system-managed — the backend rejects any scope change to
  // them via this generic update path (see IsPrivilegedKind in db/models.go),
  // since their permission set comes from a dedicated flow (circle allowlist
  // policy, JIT allocation config) instead. Submitting scopes here would
  // always fail server-side.
  const scopesReadOnly = [
    "circle_hub",
    "circle_wallet",
    "jit_hub",
    "jit_wallet",
  ].includes(app.kind ?? "");
  // A hub's own budget/expiry are user-configurable like a regular app (see
  // IsBudgetImmutableKind in db/models.go) — it's only the wallets it issues
  // (circle wallets, JIT wallets) whose limits are system-managed.
  const budgetReadOnly =
    scopesReadOnly && app.kind !== "circle_hub" && app.kind !== "jit_hub";
  // JIT/circle wallet names are system-generated (hub · identity · random) and
  // carry the identity used to resolve a Nostr profile for display — the
  // backend rejects a rename for these kinds (db.IsNameImmutableKind), so
  // don't offer the control here either.
  const nameReadOnly =
    app.kind === "jit_wallet" || app.kind === "circle_wallet";

  const handleSave = async () => {
    try {
      const updateAppRequest: UpdateAppRequest = {
        ...(!nameReadOnly && { name }),
        ...(!scopesReadOnly && {
          scopes: Array.from(permissions.scopes),
          isolated: permissions.isolated,
        }),
        ...(!budgetReadOnly && {
          budgetRenewal: permissions.budgetRenewal,
          expiresAt: permissions.expiresAt?.toISOString(),
          updateExpiresAt: true,
          maxAmount: permissions.maxAmount,
        }),
        ...(app.kind === "jit_hub" && {
          jitPerWalletMaxMloki: jitPerWalletMaxLoki * 1000,
          jitMaxExpSecs,
        }),
        ...(app.kind === "circle_hub" && {
          circleMaxExpSecs,
          circleFeesPpm,
          circlePerWalletMaxMloki: circlePerWalletMaxLoki * 1000,
          circleMinBudgetRenewal,
        }),
      };

      await request(`/api/apps/${app.id}`, {
        method: "PATCH",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify(updateAppRequest),
      });

      refetchApp();
      setIsEditingPermissions(false);
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

  // JIT/circle wallet names are baked in server-side as "<hub> · <npub prefix> · <random>"
  // (see apps.GenerateChildName). When the full identity pubkey is known, swap that
  // truncated npub segment for a resolved Nostr profile name where available.
  const identityPubkey =
    app.metadata?.identity_pubkey ?? app.metadata?.requester_pubkey;
  const { profile: identityProfile } = useNostrProfile(identityPubkey);
  const appName = React.useMemo(() => {
    const baseName =
      app.name === LOKI_ACCOUNT_APP_NAME
        ? t("connections.lokiAccount", "Loki Account")
        : app.name;
    if (!identityPubkey) {
      return baseName;
    }
    const segments = baseName.split(" · ");
    if (
      segments.length !== 3 ||
      !identityPubkey.toLowerCase().startsWith(segments[1].toLowerCase())
    ) {
      return baseName;
    }
    segments[1] = primaryProfileLabel(identityPubkey, identityProfile);
    return segments.join(" · ");
  }, [app.name, identityPubkey, identityProfile, t]);

  const { apps: appStoreApps } = useAppStore();
  const appStoreAppId = app.metadata?.app_store_app_id as string | undefined;
  const appStoreApp: AppStoreApp = React.useMemo(() => {
    const matched = appStoreAppId
      ? appStoreApps.find((suggestedApp) => suggestedApp.id === appStoreAppId)
      : undefined;
    return (
      matched ?? {
        id: appStoreAppId || "",
        title: app.name,
        description: "",
        extendedDescription: "",
        category: "misc",
        version: "",
        createdAt: 0,
        updatedAt: 0,
      }
    );
  }, [appStoreAppId, appStoreApps, app.name]);
  const connectedApps = useAppsForAppStoreApp(appStoreApp);

  const addAnotherUrl = React.useMemo(() => {
    const params = new URLSearchParams();
    params.set("name", appName);
    if (appStoreApp.id) {
      params.set("app", appStoreApp.id);
    }
    return `/apps/new?${params.toString()}`;
  }, [appName, appStoreApp.id]);

  return (
    <>
      <div className="w-full">
        <div
          className={cn(
            "flex flex-col gap-2",
            isEditingPermissions && "max-w-lg lg:max-w-none"
          )}
        >
          <AppHeader
            title={
              <div className="flex flex-col sm:flex-row gap-2 sm:items-center">
                <div className="flex flex-row gap-2 items-center min-w-0">
                  <AppAvatar app={app} className="w-10 h-10 shrink-0" />
                  <h2
                    title={appName}
                    className="min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap text-xl font-semibold"
                  >
                    {appName}
                  </h2>
                </div>
                <Badge
                  variant="positive"
                  className="flex items-center gap-1 self-start sm:self-center"
                >
                  {(connectedApps?.length || 0) > 1 ? (
                    <DropdownMenu
                      modal={false}
                      key={JSON.stringify(app) /* force reload on app change */}
                    >
                      <DropdownMenuTrigger>
                        <div className="flex items-center gap-1">
                          {t("connections.connections_count", {
                            count: connectedApps?.length,
                          })}{" "}
                          <ChevronDownIcon className="size-3 -mr-1" />
                        </div>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent className="w-56">
                        <DropdownMenuGroup>
                          {connectedApps?.map((connectedApp) => (
                            <DropdownMenuItem key={connectedApp.id}>
                              <Link
                                to={`/apps/${connectedApp.id}`}
                                className={cn(
                                  "flex flex-1 items-center gap-2",
                                  connectedApp.id === app.id && "font-semibold"
                                )}
                              >
                                {connectedApp.name}
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
            contentRight={
              <div className="flex gap-2 items-center">
                {!isEditingPermissions && (
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
                            <Link
                              to={addAnotherUrl}
                              className="flex flex-1 items-center gap-2"
                            >
                              <PlusIcon className="size-4" />{" "}
                              {t(
                                "connections.addAnother",
                                "Add Another Connection"
                              )}
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
                          {app.kind === "jit_wallet" && (
                            <DropdownMenuItem
                              asChild
                              disabled={isLoadingJitConnection}
                            >
                              <div
                                className="flex items-center gap-2"
                                onClick={handleShowJitConnection}
                              >
                                {isLoadingJitConnection ? (
                                  <Loading className="size-4" />
                                ) : (
                                  <QrCodeIcon className="size-4" />
                                )}{" "}
                                {t("circleHub.revealConnection")}
                              </div>
                            </DropdownMenuItem>
                          )}
                          <DropdownMenuSeparator />
                          <DropdownMenuItem variant="destructive" asChild>
                            <div
                              className="flex items-center gap-2"
                              onClick={() => setShowDisconnectAppDialog(true)}
                            >
                              <UnplugIcon className="size-4" />{" "}
                              {t("connections.disconnect", { appName })}
                            </div>
                          </DropdownMenuItem>
                        </DropdownMenuGroup>
                      </DropdownMenuContent>
                    </DropdownMenu>
                    {!nameReadOnly && (
                      <ResponsiveButton
                        variant="secondary"
                        onClick={() => setIsEditingPermissions(true)}
                        icon={SquarePenIcon}
                        text={t("connections.editConnection")}
                      />
                    )}
                  </>
                )}
                {isEditingPermissions && (
                  <>
                    {isEditingPermissions && (
                      <div className="flex justify-center items-center gap-2">
                        <Button
                          type="button"
                          variant="outline"
                          onClick={() => {
                            setIsEditingPermissions(false);
                          }}
                        >
                          {tc("actions.cancel", "Cancel")}
                        </Button>

                        {(app.isolated && !permissions.isolated) ||
                        (!app.scopes.includes("pay_invoice") &&
                          permissions.scopes.includes("pay_invoice")) ? (
                          <AlertDialog>
                            <AlertDialogTrigger asChild>
                              <Button type="button">
                                {tc("actions.save", "Save")}
                              </Button>
                            </AlertDialogTrigger>
                            <AlertDialogContent>
                              <AlertDialogTitle>
                                {t(
                                  "connections.confirmUpdate",
                                  "Confirm Update App"
                                )}
                              </AlertDialogTitle>
                              <AlertDialogDescription>
                                <div className="space-y-2">
                                  {app.isolated && !permissions.isolated ? (
                                    <p>
                                      <Trans
                                        i18nKey="connections.isolatedRemoveConfirm"
                                        t={t}
                                        components={{
                                          1: <span className="font-bold" />,
                                        }}
                                      >
                                        Are you sure you wish to remove the{" "}
                                        <span className="font-bold">
                                          isolated
                                        </span>{" "}
                                        status from this connection?
                                      </Trans>
                                    </p>
                                  ) : (
                                    <p>
                                      <Trans
                                        i18nKey="connections.payPermissionsConfirm"
                                        t={t}
                                        components={{
                                          1: <span className="font-bold" />,
                                        }}
                                      >
                                        Are you sure you wish to give this
                                        connection{" "}
                                        <span className="font-bold">
                                          pay permissions
                                        </span>
                                        ?
                                      </Trans>
                                    </p>
                                  )}
                                  <p className="text-amber-600 dark:text-amber-400 font-medium">
                                    {t(
                                      "connections.warningNotice",
                                      "⚠️ Warning: This applies to all apps that have this connection secret. Only change this if you know it is safe to do so, otherwise you could potentially lose all funds"
                                    )}
                                    {!!permissions.maxAmount &&
                                      t(
                                        "connections.upToBudget",
                                        " up to the specified budget"
                                      )}
                                    {permissions.isolated &&
                                      t(
                                        "connections.isolatedFunds",
                                        " that are deposited into this isolated app"
                                      )}
                                    .
                                  </p>
                                </div>
                              </AlertDialogDescription>
                              <AlertDialogFooter className="mt-5">
                                <AlertDialogCancel>
                                  {tc("actions.cancel", "Cancel")}
                                </AlertDialogCancel>
                                <Button onClick={handleSave}>
                                  {tc("actions.save", "Save")}
                                </Button>
                              </AlertDialogFooter>
                            </AlertDialogContent>
                          </AlertDialog>
                        ) : (
                          <Button onClick={handleSave}>
                            {tc("actions.save", "Save")}
                          </Button>
                        )}
                      </div>
                    )}
                  </>
                )}
              </div>
            }
            description={""}
          />
          {!isEditingPermissions && (
            <>
              {app.kind === "circle_hub" && app.circleIdentity && (
                <CircleIdentityCard
                  appId={app.id}
                  identity={app.circleIdentity}
                />
              )}
              {(app.kind === "jit_wallet" || app.kind === "circle_wallet") && (
                <ChildIdentityCard app={app} />
              )}
              {appStoreApp && (
                <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                  {!!appStoreApp?.description && (
                    <AboutAppCard appStoreApp={appStoreApp} />
                  )}
                  <AppLinksCard appStoreApp={appStoreApp} />
                </div>
              )}
              <AppUsage
                key={`${app.id}-${app.updatedAt}-${app.balance}`}
                app={app}
              />
            </>
          )}
          {isEditingPermissions &&
            !nameReadOnly &&
            app.name !== LOKI_ACCOUNT_APP_NAME && (
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
          <Card>
            <CardHeader>
              <CardTitle>
                <div className="flex flex-row justify-between items-center">
                  {t("permissions.title", "Permissions")}
                </div>
              </CardTitle>
            </CardHeader>
            <CardContent>
              {scopesReadOnly && isEditingPermissions && (
                <p className="text-sm text-muted-foreground mb-4">
                  {budgetReadOnly
                    ? t("circleHub.permissionsManagedNote")
                    : t("circleHub.budgetManagedNote")}
                </p>
              )}
              <Permissions
                capabilities={capabilities}
                permissions={
                  isEditingPermissions ? permissions : savedPermissions
                }
                setPermissions={setPermissions}
                readOnly={!isEditingPermissions}
                scopesReadOnly={scopesReadOnly}
                budgetReadOnly={budgetReadOnly}
                expiresAtReadOnly={budgetReadOnly}
                isNewConnection={false}
                budgetUsage={app.budgetUsage}
                showBudgetUsage={isEditingPermissions}
                showBudgetSection={
                  permissions.scopes.includes("pay_invoice") ||
                  app.kind === "circle_hub"
                }
                budgetCaption={
                  app.kind === "circle_hub" &&
                  t(
                    "budget.circleSharedCaption",
                    "Shared by all wallets in this circle."
                  )
                }
              />
            </CardContent>
          </Card>
          {isEditingPermissions && app.kind === "jit_hub" && (
            <JITHubConfigCard
              title={t("circleHub.hubSettingsTitle")}
              description={t("circleHub.jitHubSettingsDescription")}
              budgetLabel={t("circleHub.maxWalletBudgetLabel")}
              budgetHelper={t("circleHub.jitMaxWalletBudgetHelper")}
              expiryLabel={t("circleHub.maxWalletExpiryLabel")}
              expiryHelper={t("circleHub.jitMaxExpiryHelper")}
              perWalletMaxLoki={jitPerWalletMaxLoki}
              onPerWalletMaxLokiChange={setJitPerWalletMaxLoki}
              maxExpSecs={jitMaxExpSecs}
              onMaxExpSecsChange={setJitMaxExpSecs}
            />
          )}
          {isEditingPermissions && app.kind === "circle_hub" && (
            <Card>
              <CardHeader>
                <CardTitle>{t("circleHub.hubSettingsTitle")}</CardTitle>
                <CardDescription>
                  {t("circleHub.circleHubSettingsDescription")}
                </CardDescription>
              </CardHeader>
              <CardContent className="grid gap-4 max-w-lg">
                <div className="w-full grid gap-1.5">
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
                    min={1}
                  />
                  <p className="text-muted-foreground text-sm">
                    {t("circleHub.circleMaxWalletBudgetHelper")}
                  </p>
                </div>
                <div className="w-full grid gap-1.5">
                  <BudgetRenewalSelect
                    label={t("circleHub.minRenewalLabel")}
                    value={circleMinBudgetRenewal}
                    onChange={setCircleMinBudgetRenewal}
                  />
                  <p className="-mt-2 text-sm text-muted-foreground">
                    {t("circleHub.minRenewalHelper")}
                  </p>
                </div>
                <div className="w-full grid gap-1.5">
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
                <div className="w-full grid gap-1.5">
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
              </CardContent>
            </Card>
          )}
          {!isEditingPermissions && (
            <>
              {showConnectionDetails && (
                <ConnectionDetailsModal
                  app={app}
                  onClose={() => setShowConnectionDetails(false)}
                />
              )}
              {app.kind === "jit_wallet" && jitConnectionUri && (
                <RevealConnectionDialog
                  app={app}
                  pairingUri={jitConnectionUri}
                  onClose={() => setJitConnectionUri(undefined)}
                />
              )}
              {showDisconnectAppDialog &&
                (app.kind === "circle_hub" ? (
                  <DisconnectCircleHub
                    app={app}
                    onClose={() => setShowDisconnectAppDialog(false)}
                  />
                ) : app.kind === "jit_hub" ? (
                  <DisconnectJITHub
                    app={app}
                    onClose={() => setShowDisconnectAppDialog(false)}
                  />
                ) : (
                  <DisconnectApp
                    app={app}
                    onClose={() => setShowDisconnectAppDialog(false)}
                  />
                ))}
              {app.kind === "circle_hub" &&
                app.circleIdentity?.policy === "allowlist" && (
                  <Card>
                    <CardHeader>
                      <CardTitle>{t("circleHub.allowlistTitle")}</CardTitle>
                      {!isAllowlistFormOpen && (
                        <CardAction>
                          <ResponsiveButton
                            size="sm"
                            onClick={() =>
                              circleAllowlistRef.current?.openAdd()
                            }
                            icon={PlusIcon}
                            text={t("circleHub.addMember")}
                          />
                        </CardAction>
                      )}
                    </CardHeader>
                    <CardContent>
                      <CircleAllowlist
                        appId={app.id}
                        ref={circleAllowlistRef}
                        onFormOpenChange={setAllowlistFormOpen}
                      />
                    </CardContent>
                  </Card>
                )}
              {app.kind === "circle_hub" && (
                <Card>
                  <CardHeader>
                    <CardTitle>{t("circleHub.circleWalletsTitle")}</CardTitle>
                  </CardHeader>
                  <CardContent>
                    <CircleWallets appId={app.id} />
                  </CardContent>
                </Card>
              )}
              {app.kind === "jit_hub" && (
                <Card>
                  <CardHeader>
                    <CardTitle>{t("circleHub.jitWalletsTitle")}</CardTitle>
                    {!isJitFormOpen && (
                      <CardAction>
                        <ResponsiveButton
                          size="sm"
                          onClick={() =>
                            jitHubAllocationsRef.current?.openAdd()
                          }
                          icon={PlusIcon}
                          text={t("circleHub.addJitWallet")}
                        />
                      </CardAction>
                    )}
                  </CardHeader>
                  <CardContent>
                    <JITHubAllocations
                      appId={app.id}
                      ref={jitHubAllocationsRef}
                      onFormOpenChange={setJitFormOpen}
                    />
                  </CardContent>
                </Card>
              )}
              <AppTransactionList appId={app.id} />
            </>
          )}
        </div>
      </div>
    </>
  );
}

export default AppDetails;
