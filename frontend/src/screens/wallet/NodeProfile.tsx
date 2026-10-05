import { CircleUserRound } from "lucide-react";
import React, { useState } from "react";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import { IdentityRow, NodeIdentityRows } from "src/components/NodeIdentityRows";
import { NostrAvatar } from "src/components/NostrAvatar";
import { Avatar, AvatarFallback } from "src/components/ui/avatar";
import { Button } from "src/components/ui/button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { Skeleton } from "src/components/ui/skeleton";
import { Textarea } from "src/components/ui/textarea";
import { useNodeConnectionInfo } from "src/hooks/useNodeConnectionInfo";
import { useNodeIdentity } from "src/hooks/useNodeIdentity";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { handleRequestError } from "src/utils/handleRequestError";
import { shortenMiddle } from "src/utils/nostr";
import { request } from "src/utils/request";

type ProfileForm = {
  name: string;
  about: string;
  picture: string;
  nip05: string;
  lud16: string;
};

const emptyForm: ProfileForm = {
  name: "",
  about: "",
  picture: "",
  nip05: "",
  lud16: "",
};

export default function NodeProfile() {
  const nodeIdentity = useNodeIdentity();
  const { data: nodeConnectionInfo } = useNodeConnectionInfo();
  const {
    profile,
    isLoading: isProfileLoading,
    mutate: mutateProfile,
  } = useNostrProfile(nodeIdentity.nostrHex);

  const [isEditing, setIsEditing] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const [form, setForm] = useState<ProfileForm>(emptyForm);

  const hasProfile = !!(
    profile?.name ||
    profile?.about ||
    profile?.picture ||
    profile?.nip05 ||
    profile?.lud16
  );

  const uri =
    nodeConnectionInfo?.pubkey &&
    nodeConnectionInfo?.address &&
    nodeConnectionInfo?.port
      ? `${nodeConnectionInfo.pubkey}@${nodeConnectionInfo.address}:${nodeConnectionInfo.port}`
      : undefined;

  function startEditing() {
    setForm({
      name: profile?.name ?? "",
      about: profile?.about ?? "",
      picture: profile?.picture ?? "",
      nip05: profile?.nip05 ?? "",
      lud16: profile?.lud16 ?? "",
    });
    setIsEditing(true);
  }

  async function handleSave(e: React.FormEvent) {
    e.preventDefault();
    setIsSaving(true);
    try {
      await request("/api/node/profile", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(form),
      });

      // Relay propagation/outbox indexing isn't instant — refetching right
      // after a successful publish would likely still show the old (or no)
      // profile. Show what was just submitted immediately instead, and let
      // the next natural revalidation (focus/reconnect) pick up whatever
      // relays actually indexed.
      await mutateProfile(
        {
          name: form.name || undefined,
          about: form.about || undefined,
          picture: form.picture || undefined,
          nip05: form.nip05 || undefined,
          lud16: form.lud16 || undefined,
        },
        { revalidate: false }
      );

      toast("Profile published.");
      setIsEditing(false);
    } catch (error) {
      handleRequestError("Failed to publish profile", error);
    } finally {
      setIsSaving(false);
    }
  }

  return (
    <div className="grid gap-6">
      <AppHeader
        title="Profile"
        description="Your node's Nostr identity and published profile"
      />

      {nodeIdentity.hex && (
        <div className="max-w-lg grid gap-2">
          <Label>Node Identity</Label>
          <NodeIdentityRows identity={nodeIdentity} />
          {uri && <IdentityRow label="LN URI" value={uri} />}
        </div>
      )}

      <div className="max-w-lg grid gap-4">
        <Label>Profile</Label>

        {!isEditing ? (
          <>
            <div className="flex items-center gap-3">
              {isProfileLoading ? (
                <Skeleton className="h-14 w-14 rounded-full" />
              ) : hasProfile ? (
                <NostrAvatar
                  pubkey={nodeIdentity.nostrHex ?? ""}
                  profile={profile}
                  className="h-14 w-14"
                />
              ) : (
                // Deliberately not NostrAvatar's own npub-initials
                // fallback: that fallback is also what a profile with no
                // name set would show, so it doesn't distinguish "nothing
                // published" from "published but sparse". This is.
                <Avatar className="h-14 w-14">
                  <AvatarFallback>
                    <CircleUserRound className="h-7 w-7 text-muted-foreground" />
                  </AvatarFallback>
                </Avatar>
              )}
              <div className="min-w-0">
                {!isProfileLoading && (
                  <>
                    <div className="font-semibold truncate">
                      {profile?.name ||
                        profile?.displayName ||
                        shortenMiddle(nodeIdentity.npub ?? "")}
                    </div>
                    {hasProfile ? (
                      profile?.about && (
                        <p className="text-sm text-muted-foreground truncate">
                          {profile.about}
                        </p>
                      )
                    ) : (
                      <p className="text-sm text-muted-foreground">
                        No profile published yet
                      </p>
                    )}
                  </>
                )}
              </div>
            </div>
            <Button className="w-fit" onClick={startEditing}>
              {hasProfile ? "Edit Profile" : "Create Profile"}
            </Button>
          </>
        ) : (
          <form onSubmit={handleSave} className="grid gap-4">
            <div className="grid gap-2">
              <Label htmlFor="profile-name">Name</Label>
              <Input
                id="profile-name"
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="profile-about">About</Label>
              <Textarea
                id="profile-about"
                value={form.about}
                onChange={(e) => setForm({ ...form, about: e.target.value })}
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="profile-picture">Picture URL</Label>
              <Input
                id="profile-picture"
                type="url"
                value={form.picture}
                onChange={(e) => setForm({ ...form, picture: e.target.value })}
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="profile-nip05">NIP-05</Label>
              <Input
                id="profile-nip05"
                placeholder="you@example.com"
                value={form.nip05}
                onChange={(e) => setForm({ ...form, nip05: e.target.value })}
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="profile-lud16">Lightning Address</Label>
              <Input
                id="profile-lud16"
                placeholder="you@getalby.com"
                value={form.lud16}
                onChange={(e) => setForm({ ...form, lud16: e.target.value })}
              />
            </div>
            <div className="flex gap-2">
              <Button type="submit" disabled={isSaving}>
                {isSaving ? "Publishing..." : "Publish Profile"}
              </Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => setIsEditing(false)}
                disabled={isSaving}
              >
                Cancel
              </Button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}
