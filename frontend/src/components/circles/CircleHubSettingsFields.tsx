import { useTranslation } from "react-i18next";

import { DurationInput } from "src/components/DurationInput";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { Switch } from "src/components/ui/switch";
import { OutboundPeriod } from "src/types";

export type CircleHubSettings = {
  circleDefaultExpSecs: number;
  circlePerWalletMaxReceiveMloki: number;
  circleHoldingsCeilingMloki: number;
  circleFeeReservePpm: number;
  circleFeeReserveMinMloki: number;
  circleOutboundCeilingMloki: number;
  circleOutboundPeriod: OutboundPeriod;
  circleGraceSecs: number;
  circleDustThresholdMloki: number;
  circleRotationEnabled: boolean;
  circleRotationDelaySecs: number;
};

const OUTBOUND_PERIODS: OutboundPeriod[] = [
  "daily",
  "weekly",
  "monthly",
  "yearly",
];

// CircleHubSettingsFields edits a circle hub's NIP-CW settings (WP-13 to
// WP-18): the default wallet lifetime, the receive ceiling, the holdings
// ceiling, the routing-fee reserve, the optional outbound ceiling, the grace
// period, the dust threshold and member requests. Amounts are entered in loki
// and stored in mloki; the API refuses anything out of range.
export function CircleHubSettingsFields({
  value,
  onChange,
}: {
  value: CircleHubSettings;
  onChange: (next: CircleHubSettings) => void;
}) {
  const { t } = useTranslation("circles");
  const set = <K extends keyof CircleHubSettings>(
    key: K,
    v: CircleHubSettings[K]
  ) => onChange({ ...value, [key]: v });
  const loki = (
    key: keyof CircleHubSettings,
    labelKey:
      | "maxReceive"
      | "holdingsCeiling"
      | "feeReserveMin"
      | "outboundCeiling"
      | "dust"
  ) => (
    <div className="w-full grid gap-1.5">
      <Label htmlFor={key}>{t(`circleSettings.${labelKey}`)}</Label>
      <Input
        id={key}
        type="number"
        min={0}
        value={Math.floor((value[key] as number) / 1000)}
        onChange={(e) => set(key, Number(e.target.value) * 1000)}
      />
      <p className="text-muted-foreground text-sm">
        {t(`circleSettings.${labelKey}Helper`)}
      </p>
    </div>
  );

  return (
    <>
      <div className="w-full grid gap-1.5">
        <Label htmlFor="circleDefaultExpSecs">
          {t("circleSettings.defaultExpiry")}
        </Label>
        <DurationInput
          id="circleDefaultExpSecs"
          seconds={value.circleDefaultExpSecs}
          onChange={(s) => set("circleDefaultExpSecs", s)}
          allowNever
          neverLabel={t("circleSettings.never")}
        />
        <p className="text-muted-foreground text-sm">
          {t("circleSettings.defaultExpiryHelper")}
        </p>
      </div>
      {loki("circlePerWalletMaxReceiveMloki", "maxReceive")}
      {loki("circleHoldingsCeilingMloki", "holdingsCeiling")}
      <div className="w-full grid gap-1.5">
        <Label htmlFor="circleFeeReservePpm">
          {t("circleSettings.feeReservePpm")}
        </Label>
        <Input
          id="circleFeeReservePpm"
          type="number"
          min={0}
          value={value.circleFeeReservePpm}
          onChange={(e) => set("circleFeeReservePpm", Number(e.target.value))}
        />
      </div>
      {loki("circleFeeReserveMinMloki", "feeReserveMin")}
      {loki("circleOutboundCeilingMloki", "outboundCeiling")}
      <div className="w-full grid gap-1.5">
        <Label htmlFor="circleOutboundPeriod">
          {t("circleSettings.outboundPeriod")}
        </Label>
        <select
          id="circleOutboundPeriod"
          className="border-input h-9 rounded-md border bg-transparent px-3 text-sm"
          value={value.circleOutboundPeriod}
          onChange={(e) =>
            set("circleOutboundPeriod", e.target.value as OutboundPeriod)
          }
        >
          {OUTBOUND_PERIODS.map((p) => (
            <option key={p} value={p}>
              {t(`circleSettings.period.${p}`)}
            </option>
          ))}
        </select>
      </div>
      <div className="w-full grid gap-1.5">
        <Label htmlFor="circleGraceSecs">{t("circleSettings.grace")}</Label>
        <DurationInput
          id="circleGraceSecs"
          seconds={value.circleGraceSecs}
          onChange={(s) => set("circleGraceSecs", s)}
        />
        <p className="text-muted-foreground text-sm">
          {t("circleSettings.graceHelper")}
        </p>
      </div>
      {loki("circleDustThresholdMloki", "dust")}
      <div className="w-full grid gap-1.5">
        <div className="flex items-center gap-3">
          <Switch
            id="circleRotationEnabled"
            checked={value.circleRotationEnabled}
            onCheckedChange={(c) => set("circleRotationEnabled", c)}
          />
          <Label htmlFor="circleRotationEnabled">
            {t("circleSettings.rotation")}
          </Label>
        </div>
        <p className="text-muted-foreground text-sm">
          {t("circleSettings.rotationHelper")}
        </p>
      </div>
      {value.circleRotationEnabled && (
        <div className="w-full grid gap-1.5">
          <Label htmlFor="circleRotationDelaySecs">
            {t("circleSettings.rotationDelay")}
          </Label>
          <DurationInput
            id="circleRotationDelaySecs"
            seconds={value.circleRotationDelaySecs}
            onChange={(s) => set("circleRotationDelaySecs", s)}
          />
        </div>
      )}
    </>
  );
}
