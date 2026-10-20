import { PencilIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { useSWRConfig } from "swr";

import { FollowSetFields } from "src/components/circles/FollowSetFields";
import { Button } from "src/components/ui/button";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "src/components/ui/dialog";
import { FollowSetRequest, FollowSetResponse } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

// FollowSetEditDialog publishes a new version of a follow set this hub
// authors. Every save replaces the whole set, so the form starts from the
// version the hub last read.
export function FollowSetEditDialog({
  identityId,
  followSet,
}: {
  identityId: number;
  followSet: FollowSetResponse;
}) {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const { mutate } = useSWRConfig();
  const [open, setOpen] = React.useState(false);
  const [isSaving, setSaving] = React.useState(false);
  const [draft, setDraft] = React.useState<FollowSetRequest>(() =>
    fromResponse(followSet)
  );

  const handleOpenChange = (next: boolean) => {
    if (next) {
      setDraft(fromResponse(followSet));
    }
    setOpen(next);
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      await request(`/api/circle-identities/${identityId}/follow-set`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          ...draft,
          title: draft.title.trim(),
          description: draft.description.trim(),
        }),
      });
      // The set feeds the identity card, the app's member count and the
      // circle list, so refresh every key that reads it.
      await mutate(
        (key) =>
          typeof key === "string" &&
          (key.startsWith(`/api/circle-identities/${identityId}`) ||
            key.startsWith("/api/apps"))
      );
      toast(t("followSet.savedToast"));
      setOpen(false);
    } catch (error) {
      handleRequestError(t("followSet.errors.publish"), error);
    }
    setSaving(false);
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <PencilIcon className="size-3.5" /> {t("followSet.editTrigger")}
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("followSet.editTitle")}</DialogTitle>
          <DialogDescription>
            {t("followSet.editDescription")}
          </DialogDescription>
        </DialogHeader>
        <FollowSetFields
          value={draft}
          onChange={setDraft}
          authorPubkeyHex={followSet.providerPubkey}
          hiddenMembersAvailable={followSet.hiddenMembersAvailable}
        />
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            {tc("actions.cancel")}
          </Button>
          <LoadingButton loading={isSaving} onClick={handleSave}>
            {t("followSet.save")}
          </LoadingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function fromResponse(followSet: FollowSetResponse): FollowSetRequest {
  return {
    title: followSet.title,
    description: followSet.description,
    publicMembers: followSet.publicMembers,
    hiddenMembers: followSet.hiddenMembers,
  };
}
