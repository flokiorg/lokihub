import { MegaphoneIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Input } from "src/components/ui/input";
import { setListingRelays, useListingRelays } from "src/hooks/useListings";
import { handleRequestError } from "src/utils/handleRequestError";

// Where this node publishes its listings (and what its relay list names as
// write relays). A node-level setting, so it lives here in Services beside the
// other relays — not on the operational Listings page. Left blank, listings go
// to the hub's general relays, so most hosts never touch this.
export function ListingRelaysCard({ className }: { className?: string }) {
  const { t } = useTranslation("listings");
  const { data, mutate } = useListingRelays();
  const [value, setValue] = React.useState<string | undefined>();

  if (!data) {
    return null;
  }

  const current = value ?? data.configured.join(", ");
  const save = async () => {
    try {
      await setListingRelays(
        current
          .split(/[\s,]+/)
          .map((r) => r.trim())
          .filter((r) => r)
      );
      await mutate();
      setValue(undefined);
      toast(t("relays.saved"));
    } catch (error) {
      handleRequestError(t("errors.relays"), error);
    }
  };

  return (
    <Card className={className}>
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2 text-lg">
          <MegaphoneIcon className="h-5 w-5 text-primary" />
          {t("relays.title")}
        </CardTitle>
        <CardDescription>
          {t("relays.description", { relays: data.effective.join(", ") })}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex gap-2">
        <Input
          aria-label={t("relays.title")}
          placeholder="wss://"
          value={current}
          onChange={(e) => setValue(e.target.value)}
        />
        <Button onClick={save}>{t("relays.save")}</Button>
      </CardContent>
    </Card>
  );
}
