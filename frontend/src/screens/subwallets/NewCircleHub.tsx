import { Check, Globe, ListChecks, Server, UserPlus } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import BudgetRenewalSelect from "src/components/BudgetRenewalSelect";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "src/components/ui/accordion";
import { Button } from "src/components/ui/button";
import { CurrencyInput } from "src/components/CurrencyInput";
import { DurationInput } from "src/components/DurationInput";
import ExpirySelect from "src/components/ExpirySelect";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "src/components/ui/select";
import { FollowSetFields } from "src/components/circles/FollowSetFields";
import { ManageIdentitiesDialog } from "src/components/circles/ManageIdentitiesDialog";
import { MemberPicker } from "src/components/circles/MemberPicker";
import { NostrPubkeyInput } from "src/components/circles/NostrPubkeyInput";
import { WarningAlert } from "src/components/circles/WarningAlert";
import { SUBWALLET_APPSTORE_APP_ID, WEEK_SCALE_PRESETS } from "src/constants";
import { useCircleIdentities } from "src/hooks/useCircleIdentities";
import { useInfo } from "src/hooks/useInfo";
import { useNodeIdentity } from "src/hooks/useNodeIdentity";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { useInputUnit, useUnit } from "src/hooks/useUnit";
import { cn } from "src/lib/utils";
import { createApp } from "src/requests/createApp";
import {
  BudgetRenewalType,
  CreateAppRequest,
  FollowSetRequest,
} from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

// UI never shows raw backend policy names ("follow_set"/"allowlist") — the
// mode selector below uses outcome-focused copy and maps to these internally.
type MemberMode = "followSet" | "pinned";
// Where a follow set comes from: published by this hub as its node identity,
// or published by another account and only read here.
type FollowSetSource = "managed" | "external";

const EMPTY_FOLLOW_SET: FollowSetRequest = {
  title: "",
  description: "",
  publicMembers: [],
  hiddenMembers: [],
};

type OptionCard<T extends string> = {
  value: T;
  icon: typeof UserPlus;
  title: string;
  description: string;
};

function OptionCards<T extends string>({
  options,
  value,
  onChange,
}: {
  options: OptionCard<T>[];
  value: T;
  onChange: (value: T) => void;
}) {
  return (
    <div className="grid grid-cols-2 gap-3">
      {options.map(({ value: option, icon: Icon, title, description }) => {
        const isSelected = value === option;
        return (
          <button
            type="button"
            key={option}
            onClick={() => onChange(option)}
            className={cn(
              "relative flex flex-col gap-2 rounded-lg border p-3 text-start transition-all",
              isSelected
                ? "border-primary ring-1 ring-primary shadow-sm"
                : "border-border hover:border-primary/50"
            )}
          >
            {isSelected && (
              <div className="absolute top-2 end-2 rounded-full bg-primary p-0.5 text-primary-foreground">
                <Check className="h-3 w-3" />
              </div>
            )}
            <Icon
              className={cn(
                "h-4 w-4",
                isSelected ? "text-primary" : "text-muted-foreground"
              )}
            />
            <span className="text-sm font-semibold">{title}</span>
            <p className="text-xs leading-snug text-muted-foreground">
              {description}
            </p>
          </button>
        );
      })}
    </div>
  );
}

export function NewCircleHub() {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  const { data: identitiesData } = useCircleIdentities();
  const identities = identitiesData?.identities ?? [];
  const hasSavedIdentities = identities.length > 0;
  const { nostrHex: nodePubkeyHex } = useNodeIdentity();
  const { data: info } = useInfo();
  const hiddenMembersAvailable = !!info?.privateTransport?.nodeIdentity;

  const [name, setName] = React.useState("");
  const [identityMode, setIdentityMode] = React.useState<"new" | "existing">(
    "new"
  );
  const [selectedIdentityId, setSelectedIdentityId] = React.useState<
    string | undefined
  >(undefined);
  const [identityName, setIdentityName] = React.useState("");
  const [providerPubkeyInput, setProviderPubkeyInput] = React.useState("");
  const [resolvedPubkeyHex, setResolvedPubkeyHex] = React.useState<
    string | undefined
  >(undefined);
  const [memberMode, setMemberMode] = React.useState<MemberMode>("followSet");
  const [followSetSource, setFollowSetSource] =
    React.useState<FollowSetSource>("managed");
  const [followSetD, setFollowSetD] = React.useState("");
  const [followSet, setFollowSet] =
    React.useState<FollowSetRequest>(EMPTY_FOLLOW_SET);
  const [selectedMemberPubkeys, setSelectedMemberPubkeys] = React.useState<
    string[]
  >([]);
  const [maxExpSecs, setMaxExpSecs] = React.useState(2_592_000); // 30 days
  const [feesPpm, setFeesPpm] = React.useState(0);
  const [perWalletMaxLoki, setPerWalletMaxLoki] = React.useState(1000);
  const [minBudgetRenewal, setMinBudgetRenewal] =
    React.useState<BudgetRenewalType>("monthly");
  const [hubMaxAmountLoki, setHubMaxAmountLoki] = React.useState(0);
  const [hubBudgetRenewal, setHubBudgetRenewal] =
    React.useState<BudgetRenewalType>("never");
  const [hubExpiresAt, setHubExpiresAt] = React.useState<Date | undefined>(
    undefined
  );
  const [isLoading, setLoading] = React.useState(false);

  const { scaleInputAmount, parseInputAmount } = useUnit();
  const [inputUnit, setInputUnit] = useInputUnit(perWalletMaxLoki);

  const { profile: ownerProfile } = useNostrProfile(resolvedPubkeyHex);

  const isNewIdentity = identityMode === "new" || !hasSavedIdentities;
  const isManagedFollowSet =
    memberMode === "followSet" && followSetSource === "managed";
  // A set this hub publishes is signed by the node identity; every other
  // mode names its account explicitly.
  const needsProviderInput = !isManagedFollowSet;

  // Single source of truth for what blocks submission — isSubmitDisabled and
  // submitHint both derive from this list. A rule's `hint` is left undefined
  // when its own inline message elsewhere on the form already explains it.
  const validationIssues: { blocked: boolean; hint?: string }[] = !isNewIdentity
    ? [
        {
          blocked: !selectedIdentityId,
          hint: t("newCircleHub.selectExistingHint"),
        },
      ]
    : [
        { blocked: !name, hint: t("newCircleHub.enterNameHint") },
        { blocked: needsProviderInput && !resolvedPubkeyHex },
        { blocked: isManagedFollowSet && !nodePubkeyHex },
        {
          blocked: memberMode === "followSet" && !followSetD.trim(),
          hint: t("newCircleHub.enterDHint"),
        },
        {
          blocked:
            memberMode === "pinned" && selectedMemberPubkeys.length === 0,
        },
      ];

  const isSubmitDisabled = validationIssues.some((issue) => issue.blocked);
  const submitHint = validationIssues.find(
    (issue) => issue.blocked && issue.hint
  )?.hint;

  const identityNamePlaceholder =
    (isManagedFollowSet && followSet.title) ||
    ownerProfile?.displayName ||
    ownerProfile?.name ||
    name ||
    t("newCircleHub.identityNamePlaceholder");

  const newIdentityFields = (): Partial<CreateAppRequest> => {
    const base = {
      circleIdentityName: identityName || identityNamePlaceholder,
    };
    if (memberMode === "pinned") {
      return {
        ...base,
        circlePolicy: "allowlist",
        providerPubkey: resolvedPubkeyHex,
      };
    }
    if (followSetSource === "external") {
      return {
        ...base,
        circlePolicy: "follow_set",
        providerPubkey: resolvedPubkeyHex,
        followSetD: followSetD.trim(),
      };
    }
    // An empty provider means the node identity: the hub publishes the set
    // before it creates the circle.
    return {
      ...base,
      circlePolicy: "follow_set",
      followSetD: followSetD.trim(),
      followSet: {
        ...followSet,
        title: followSet.title.trim(),
        description: followSet.description.trim(),
      },
    };
  };

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setLoading(true);
    try {
      const req: CreateAppRequest = {
        name,
        kind: "circle_hub",
        scopes: ["circle_wallet"],
        circleMaxExpSecs: maxExpSecs,
        circleFeesPpm: feesPpm,
        circlePerWalletMaxMloki: perWalletMaxLoki * 1000,
        circleMinBudgetRenewal: minBudgetRenewal,
        ...(hubMaxAmountLoki > 0 && {
          maxAmount: hubMaxAmountLoki,
          budgetRenewal: hubBudgetRenewal,
        }),
        ...(hubExpiresAt && { expiresAt: hubExpiresAt.toISOString() }),
        metadata: { app_store_app_id: SUBWALLET_APPSTORE_APP_ID },
        ...(!isNewIdentity
          ? { circleIdentityId: Number(selectedIdentityId) }
          : newIdentityFields()),
      };
      const response = await createApp(req);

      if (
        isNewIdentity &&
        memberMode === "pinned" &&
        selectedMemberPubkeys.length > 0
      ) {
        try {
          await request(`/api/apps/${response.id}/circle/allowlist`, {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ pubkeys: selectedMemberPubkeys }),
          });
        } catch (allowlistError) {
          toast.error(t("newCircleHub.allowlistSeedFailedToast", { name }));
          navigate("/sub-wallets/created", { state: response });
          setLoading(false);
          return;
        }
      }

      navigate("/sub-wallets/created", { state: response });
      toast(t("newCircleHub.createdToast", { name }));
    } catch (error) {
      handleRequestError(t("newCircleHub.errors.create"), error);
    }
    setLoading(false);
  };

  const memberModeOptions: OptionCard<MemberMode>[] = [
    {
      value: "followSet",
      icon: ListChecks,
      title: t("newCircleHub.modeFollowSetTitle"),
      description: t("newCircleHub.modeFollowSetDescription"),
    },
    {
      value: "pinned",
      icon: UserPlus,
      title: t("newCircleHub.modePinnedTitle"),
      description: t("newCircleHub.modePinnedDescription"),
    },
  ];
  const followSetSourceOptions: OptionCard<FollowSetSource>[] = [
    {
      value: "managed",
      icon: Server,
      title: t("followSet.sourceManagedTitle"),
      description: t("followSet.sourceManagedDescription"),
    },
    {
      value: "external",
      icon: Globe,
      title: t("followSet.sourceExternalTitle"),
      description: t("followSet.sourceExternalDescription"),
    },
  ];

  const isFormLocked = needsProviderInput && !resolvedPubkeyHex;

  return (
    <div className="grid gap-5">
      <AppHeader
        title={t("newCircleHub.title")}
        description={t("newCircleHub.description")}
      />
      <form
        onSubmit={handleSubmit}
        className="flex flex-col items-start gap-6 max-w-lg"
      >
        <div className="w-full grid gap-1.5">
          <Label htmlFor="name">{t("common.nameLabel")}</Label>
          <Input
            autoFocus
            id="name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
            autoComplete="off"
          />
        </div>

        {hasSavedIdentities && (
          <div className="w-full grid gap-1.5">
            <div className="flex items-center justify-between">
              <Label htmlFor="identity">
                {t("newCircleHub.identityModeLabel")}
              </Label>
              <ManageIdentitiesDialog />
            </div>
            <Select
              value={isNewIdentity ? "new" : selectedIdentityId}
              onValueChange={(v) => {
                if (v === "new") {
                  setIdentityMode("new");
                  setSelectedIdentityId(undefined);
                } else {
                  setIdentityMode("existing");
                  setSelectedIdentityId(v);
                }
              }}
            >
              <SelectTrigger id="identity" className="w-full">
                <SelectValue
                  placeholder={t("newCircleHub.selectIdentityPlaceholder")}
                />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="new">
                  {t("newCircleHub.createNewIdentity")}
                </SelectItem>
                <SelectSeparator />
                <SelectGroup>
                  <SelectLabel>
                    {t("newCircleHub.existingIdentities")}
                  </SelectLabel>
                  {identities.map((identity) => (
                    <SelectItem key={identity.id} value={String(identity.id)}>
                      <span className="flex w-full items-center justify-between gap-2">
                        <span>
                          {identity.name} ({t(`policyLabel.${identity.policy}`)}
                          )
                        </span>
                        {identity.usedByCount > 0 && (
                          <span className="text-xs text-muted-foreground">
                            {t("common.inUse")}
                          </span>
                        )}
                      </span>
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-sm">
              {t("newCircleHub.identityReuseHelper")}
            </p>
          </div>
        )}

        {isNewIdentity ? (
          <div className="w-full grid gap-4 rounded-lg border p-4">
            <div className="w-full grid gap-1.5">
              <Label>{t("newCircleHub.memberModeLabel")}</Label>
              <OptionCards
                options={memberModeOptions}
                value={memberMode}
                onChange={setMemberMode}
              />
            </div>

            {memberMode === "followSet" && (
              <div className="w-full grid gap-1.5">
                <Label>{t("followSet.sourceLabel")}</Label>
                <OptionCards
                  options={followSetSourceOptions}
                  value={followSetSource}
                  onChange={setFollowSetSource}
                />
              </div>
            )}

            {needsProviderInput && (
              <NostrPubkeyInput
                id="providerPubkey"
                value={providerPubkeyInput}
                onChange={setProviderPubkeyInput}
                onResolved={setResolvedPubkeyHex}
                {...(memberMode === "followSet" && {
                  label: t("followSet.externalProviderLabel"),
                  helperText: t("followSet.externalProviderHelper"),
                })}
              />
            )}

            <div
              className={cn(
                "w-full grid gap-4",
                isFormLocked && "opacity-50 pointer-events-none"
              )}
            >
              {isFormLocked && (
                <p className="text-sm text-muted-foreground -mt-2">
                  {t("newCircleHub.enterIdentityHelper")}
                </p>
              )}

              {memberMode === "followSet" && (
                <div className="w-full grid gap-1.5">
                  <Label htmlFor="followSetD">{t("followSet.dLabel")}</Label>
                  <Input
                    id="followSetD"
                    value={followSetD}
                    maxLength={64}
                    onChange={(e) => setFollowSetD(e.target.value)}
                    placeholder={t("followSet.dPlaceholder")}
                    autoComplete="off"
                  />
                  <p className="text-sm text-muted-foreground">
                    {isManagedFollowSet
                      ? t("followSet.dHelper")
                      : t("followSet.dExternalHelper")}
                  </p>
                </div>
              )}

              {isManagedFollowSet && (
                <FollowSetFields
                  value={followSet}
                  onChange={setFollowSet}
                  authorPubkeyHex={nodePubkeyHex}
                  hiddenMembersAvailable={hiddenMembersAvailable}
                />
              )}

              {memberMode === "followSet" && !isManagedFollowSet && (
                <WarningAlert>{t("followSet.externalWarning")}</WarningAlert>
              )}

              {memberMode === "pinned" && (
                <>
                  <MemberPicker
                    ownerPubkeyHex={resolvedPubkeyHex}
                    selected={selectedMemberPubkeys}
                    onChange={setSelectedMemberPubkeys}
                  />
                  {selectedMemberPubkeys.length === 0 && (
                    <p className="text-sm text-muted-foreground">
                      {t("newCircleHub.pinMemberHelper")}
                    </p>
                  )}
                </>
              )}

              <div className="w-full grid gap-1.5">
                <Label htmlFor="identityName">
                  {t("newCircleHub.identityNameLabel")}
                </Label>
                <Input
                  id="identityName"
                  type="text"
                  value={identityName}
                  onChange={(e) => setIdentityName(e.target.value)}
                  placeholder={identityNamePlaceholder}
                  autoComplete="off"
                />
                <p className="text-muted-foreground text-sm">
                  {t("newCircleHub.identityNameHelper")}
                </p>
              </div>
            </div>
          </div>
        ) : null}

        <Accordion type="single" collapsible className="w-full">
          <AccordionItem value="advanced">
            <AccordionTrigger className="text-sm">
              {t("common.advancedSettings")}
            </AccordionTrigger>
            <AccordionContent className="grid gap-4">
              <div className="w-full grid gap-1.5">
                <Label htmlFor="perWalletMax">
                  {t("common.maxWalletBudgetLabel")}
                </Label>
                <CurrencyInput
                  id="perWalletMax"
                  amount={
                    perWalletMaxLoki
                      ? scaleInputAmount(perWalletMaxLoki, inputUnit).toString()
                      : ""
                  }
                  onAmountChange={(val) =>
                    setPerWalletMaxLoki(
                      parseInputAmount(parseFloat(val) || 0, inputUnit)
                    )
                  }
                  inputUnit={inputUnit}
                  onInputUnitChange={setInputUnit}
                  required
                  min={1}
                />
                <p className="text-muted-foreground text-sm">
                  {t("newCircleHub.maxWalletBudgetHelper")}
                </p>
              </div>
              <div className="w-full grid gap-1.5">
                <BudgetRenewalSelect
                  label={t("common.minRenewalLabel")}
                  value={minBudgetRenewal}
                  onChange={setMinBudgetRenewal}
                />
                <p className="-mt-2 text-sm text-muted-foreground">
                  {t("common.minRenewalHelper")}
                </p>
              </div>
              <div className="w-full grid gap-1.5">
                <Label htmlFor="maxExpSecs">
                  {t("common.maxWalletExpiryLabel")}
                </Label>
                <DurationInput
                  id="maxExpSecs"
                  seconds={maxExpSecs}
                  onChange={setMaxExpSecs}
                  min={60}
                  presets={WEEK_SCALE_PRESETS}
                />
                <p className="text-muted-foreground text-sm">
                  {t("newCircleHub.maxExpiryHelper")}
                </p>
              </div>
              <div className="w-full grid gap-1.5">
                <Label htmlFor="feesPpm">{t("common.feePpmLabel")}</Label>
                <Input
                  id="feesPpm"
                  type="number"
                  min={0}
                  value={feesPpm}
                  onChange={(e) => setFeesPpm(Number(e.target.value))}
                />
                <p className="text-muted-foreground text-sm">
                  {t("common.feePpmHelper")}
                </p>
              </div>
              <div className="w-full grid gap-1.5">
                <Label htmlFor="hubMaxAmount">
                  {t("newCircleHub.hubBudgetLabel")}
                </Label>
                <CurrencyInput
                  id="hubMaxAmount"
                  amount={
                    hubMaxAmountLoki
                      ? scaleInputAmount(hubMaxAmountLoki, inputUnit).toString()
                      : ""
                  }
                  onAmountChange={(val) =>
                    setHubMaxAmountLoki(
                      parseInputAmount(parseFloat(val) || 0, inputUnit)
                    )
                  }
                  inputUnit={inputUnit}
                  onInputUnitChange={setInputUnit}
                  min={0}
                />
                <p className="text-muted-foreground text-sm">
                  {t("newCircleHub.hubBudgetHelper")}
                </p>
              </div>
              {hubMaxAmountLoki > 0 && (
                <div className="w-full grid gap-1.5">
                  <BudgetRenewalSelect
                    label={t("newCircleHub.hubBudgetRenewalLabel")}
                    value={hubBudgetRenewal}
                    onChange={setHubBudgetRenewal}
                  />
                  <p className="-mt-2 text-sm text-muted-foreground">
                    {t("newCircleHub.hubBudgetRenewalHelper")}
                  </p>
                </div>
              )}
              <div className="w-full grid gap-1.5">
                <ExpirySelect
                  label={t("newCircleHub.hubExpiryLabel")}
                  value={hubExpiresAt}
                  onChange={setHubExpiresAt}
                />
                <p className="-mt-2 text-sm text-muted-foreground">
                  {t("newCircleHub.hubExpiryHelper")}
                </p>
              </div>
            </AccordionContent>
          </AccordionItem>
        </Accordion>

        <div className="grid gap-2">
          <div className="flex gap-3">
            <Button
              type="button"
              variant="outline"
              onClick={() => navigate(-1)}
            >
              {tc("actions.cancel")}
            </Button>
            <LoadingButton
              loading={isLoading}
              type="submit"
              disabled={isSubmitDisabled}
            >
              {t("newCircleHub.submit")}
            </LoadingButton>
          </div>
          {submitHint && (
            <p className="text-sm text-muted-foreground">{submitHint}</p>
          )}
        </div>
      </form>
    </div>
  );
}
