import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Label } from "src/components/ui/label";
import { Switch } from "src/components/ui/switch";
import { Textarea } from "src/components/ui/textarea";
import { App, CircleMemberRequestResult } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

// CircleAcceptingCard is the circle's "Close to new members" toggle (NIP-CW
// §The Circle Wallet Hub Connection, accepting): while closed every join is
// refused, and members keep their wallets and lookups.
export function CircleAcceptingCard({
  app,
  onChanged,
}: {
  app: App;
  onChanged: () => void;
}) {
  const { t } = useTranslation("circles");
  const [saving, setSaving] = React.useState(false);
  const accepting = app.circleAccepting ?? true;
  const tornDown = app.circleTeardownRequestedAt !== undefined;

  const toggle = async (next: boolean) => {
    setSaving(true);
    try {
      await request(`/api/apps/${app.id}/circle/accepting`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ accepting: next }),
      });
      toast(
        next ? t("circleControls.openedToast") : t("circleControls.closedToast")
      );
      onChanged();
    } catch (error) {
      handleRequestError(t("circleControls.errors.accepting"), error);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("circleControls.acceptingTitle")}</CardTitle>
        <CardDescription>
          {tornDown
            ? t("circleControls.tornDown")
            : t("circleControls.acceptingDescription")}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex items-center gap-3">
        <Switch
          id="circleAccepting"
          checked={accepting}
          disabled={saving || tornDown}
          onCheckedChange={toggle}
        />
        <Label htmlFor="circleAccepting">
          {accepting
            ? t("circleControls.accepting")
            : t("circleControls.notAccepting")}
        </Label>
      </CardContent>
    </Card>
  );
}

// CircleMemberRequestCard submits a member request the member signed (NIP-CW
// §Recovering a Lost Connection): the host pastes the signed kind-23199 event,
// and the Hub's answer is shown as it comes, its NIP-CW code included. The Hub
// never acts on the host's word alone.
export function CircleMemberRequestCard({ app }: { app: App }) {
  const { t } = useTranslation("circles");
  const [eventJSON, setEventJSON] = React.useState("");
  const [submitting, setSubmitting] = React.useState(false);
  const [result, setResult] = React.useState<CircleMemberRequestResult | null>(
    null
  );

  if (!app.circleRotationEnabled) {
    return null;
  }

  const submit = async () => {
    let event: unknown;
    try {
      event = JSON.parse(eventJSON);
    } catch {
      toast.error(t("circleControls.errors.notJSON"));
      return;
    }
    setSubmitting(true);
    try {
      const res = await request<CircleMemberRequestResult>(
        `/api/circle-hubs/${app.id}/member-requests`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ event }),
        }
      );
      setResult(res ?? null);
      if (res && !res.code) {
        setEventJSON("");
      }
    } catch (error) {
      handleRequestError(t("circleControls.errors.memberRequest"), error);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("circleControls.memberRequestTitle")}</CardTitle>
        <CardDescription>
          {t("circleControls.memberRequestDescription")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <Textarea
          aria-label={t("circleControls.memberRequestTitle")}
          placeholder='{"kind":23199,...}'
          className="font-mono text-xs"
          value={eventJSON}
          onChange={(e) => setEventJSON(e.target.value)}
        />
        <div>
          <LoadingButton
            loading={submitting}
            disabled={eventJSON.trim() === ""}
            onClick={submit}
          >
            {t("circleControls.submit")}
          </LoadingButton>
        </div>
        {result && (
          <p
            className={
              result.code
                ? "text-sm text-destructive"
                : "text-sm text-muted-foreground"
            }
            data-testid="member-request-result"
          >
            {result.code
              ? t("circleControls.refused", {
                  code: result.code,
                  message: result.message,
                })
              : result.completes_at
                ? t("circleControls.rotationPending", {
                    at: new Date(result.completes_at * 1000).toLocaleString(),
                  })
                : t("circleControls.applied", { action: result.action })}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
