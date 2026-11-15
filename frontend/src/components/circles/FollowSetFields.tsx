import { useTranslation } from "react-i18next";

import { MemberPicker } from "src/components/circles/MemberPicker";
import { WarningAlert } from "src/components/circles/WarningAlert";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { Textarea } from "src/components/ui/textarea";
import { FollowSetRequest } from "src/types";

// FollowSetFields edits the parts of a follow set this hub publishes (NIP-CW
// §Membership): its title, description, and members split into public `p`
// tags and `hidden_p` entries only the hub can read. Shared by circle
// creation and the edit dialog so both publish the same shape.
export function FollowSetFields({
  value,
  onChange,
  authorPubkeyHex,
  hiddenMembersAvailable,
}: {
  value: FollowSetRequest;
  onChange: (value: FollowSetRequest) => void;
  // The node identity that signs the set. MemberPicker needs an owner to
  // enable search and to keep the author out of its own results.
  authorPubkeyHex: string | undefined;
  // Hidden members need the private transport. When it isn't running, the
  // hidden picker is replaced by a warning.
  hiddenMembersAvailable: boolean;
}) {
  const { t } = useTranslation("circles");

  return (
    <div className="grid w-full gap-4">
      <WarningAlert>{t("followSet.publicWarning")}</WarningAlert>

      <div className="grid w-full gap-1.5">
        <Label htmlFor="followSetTitle">{t("followSet.titleLabel")}</Label>
        <Input
          id="followSetTitle"
          value={value.title}
          maxLength={255}
          onChange={(e) => onChange({ ...value, title: e.target.value })}
          placeholder={t("followSet.titlePlaceholder")}
          autoComplete="off"
        />
        <p className="text-sm text-muted-foreground">
          {t("followSet.titleHelper")}
        </p>
      </div>

      <div className="grid w-full gap-1.5">
        <Label htmlFor="followSetDescription">
          {t("followSet.descriptionLabel")}
        </Label>
        <Textarea
          id="followSetDescription"
          value={value.description}
          maxLength={1000}
          onChange={(e) => onChange({ ...value, description: e.target.value })}
        />
      </div>

      <div className="grid w-full gap-1.5">
        <Label>{t("followSet.publicMembersLabel")}</Label>
        <MemberPicker
          ownerPubkeyHex={authorPubkeyHex}
          selected={value.publicMembers}
          onChange={(publicMembers) => onChange({ ...value, publicMembers })}
          excludePubkeys={value.hiddenMembers}
        />
        <p className="text-sm text-muted-foreground">
          {t("followSet.publicMembersHelper")}
        </p>
      </div>

      <div className="grid w-full gap-1.5">
        <Label>{t("followSet.hiddenMembersLabel")}</Label>
        {hiddenMembersAvailable ? (
          <>
            <MemberPicker
              ownerPubkeyHex={authorPubkeyHex}
              selected={value.hiddenMembers}
              onChange={(hiddenMembers) =>
                onChange({ ...value, hiddenMembers })
              }
              excludePubkeys={value.publicMembers}
            />
            <p className="text-sm text-muted-foreground">
              {t("followSet.hiddenMembersHelper")}
            </p>
          </>
        ) : (
          <WarningAlert>{t("followSet.hiddenUnavailable")}</WarningAlert>
        )}
      </div>
    </div>
  );
}
