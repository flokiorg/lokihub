import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import { CircleUserRound } from "lucide-react";
import React, { useState } from "react";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import { IdentityRow, NodeIdentityRows } from "src/components/NodeIdentityRows";
import { NostrAvatar } from "src/components/NostrAvatar";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "src/components/ui/accordion";
import { Avatar, AvatarFallback } from "src/components/ui/avatar";
import { Button } from "src/components/ui/button";
import { Card, CardContent } from "src/components/ui/card";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { Skeleton } from "src/components/ui/skeleton";
import { Textarea } from "src/components/ui/textarea";
import { useNodeConnectionInfo } from "src/hooks/useNodeConnectionInfo";
import { useNodeIdentity } from "src/hooks/useNodeIdentity";
import { useNostrNotes } from "src/hooks/useNostrNotes";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { handleRequestError } from "src/utils/handleRequestError";
import { shortenMiddle } from "src/utils/nostr";
import { request } from "src/utils/request";

dayjs.extend(relativeTime);

type ProfileForm = {
  name: string;
  about: string;
  picture: string;
  banner: string;
  nip05: string;
  lud16: string;
};

const emptyForm: ProfileForm = {
  name: "",
  about: "",
  picture: "",
  banner: "",
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
  const {
    notes,
    isLoading: isNotesLoading,
    isLoadingMore: isLoadingMoreNotes,
    hasMore: hasMoreNotes,
    loadMore: loadMoreNotes,
  } = useNostrNotes(nodeIdentity.nostrHex);

  const [isEditing, setIsEditing] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const [form, setForm] = useState<ProfileForm>(emptyForm);

  const hasProfile = !!(
    profile?.name ||
    profile?.about ||
    profile?.picture ||
    profile?.banner ||
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
      banner: profile?.banner ?? "",
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
          banner: form.banner || undefined,
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
    <div className="grid gap-6 max-w-2xl">
      <AppHeader
        title="Profile"
        description="Your node's Nostr identity and published profile"
      />

      {!isEditing ? (
        <>
          {/* Banner, with the avatar pulled up over its bottom edge — same
              banner-behind-overlapping-avatar convention a social profile
              page uses, just without the extra chrome (follow/zap/share)
              this page has no use for. */}
          <div>
            <div
              className="h-40 w-full rounded-t-md bg-cover bg-center"
              style={{
                backgroundImage: profile?.banner
                  ? `url(${profile.banner})`
                  : "linear-gradient(135deg, var(--muted) 0%, transparent 100%)",
              }}
            />
            <div className="flex items-end gap-3 px-4 -mt-10">
              {isProfileLoading ? (
                <Skeleton className="h-20 w-20 rounded-full border-4 border-background" />
              ) : hasProfile ? (
                <NostrAvatar
                  pubkey={nodeIdentity.nostrHex ?? ""}
                  profile={profile}
                  className="h-20 w-20 border-4 border-background"
                />
              ) : (
                // Deliberately not NostrAvatar's own npub-initials fallback:
                // that fallback is also what a profile with no name set
                // would show, so it doesn't distinguish "nothing published"
                // from "published but sparse". This is.
                <Avatar className="h-20 w-20 border-4 border-background">
                  <AvatarFallback>
                    <CircleUserRound className="h-10 w-10 text-muted-foreground" />
                  </AvatarFallback>
                </Avatar>
              )}
              <div className="min-w-0 pb-1 flex-1">
                {!isProfileLoading && (
                  <div className="font-semibold text-lg truncate">
                    {profile?.name ||
                      profile?.displayName ||
                      shortenMiddle(nodeIdentity.npub ?? "")}
                  </div>
                )}
              </div>
              <Button onClick={startEditing} className="mb-1">
                {hasProfile ? "Edit Profile" : "Create Profile"}
              </Button>
            </div>
          </div>

          <div className="grid gap-4 px-4">
            {!isProfileLoading &&
              (hasProfile ? (
                profile?.about && (
                  <p className="text-sm whitespace-pre-wrap">{profile.about}</p>
                )
              ) : (
                <p className="text-sm text-muted-foreground">
                  No profile published yet
                </p>
              ))}

            {/* Identity — npub always, nip05/lud16 only once published. Each
                carries a QR action (IdentityRow's qr prop) so a visitor can
                scan it with another device instead of copy/pasting. */}
            {nodeIdentity.npub && (
              <IdentityRow label="Nostr npub" value={nodeIdentity.npub} qr />
            )}
            {profile?.nip05 && (
              <IdentityRow label="NIP-05" value={profile.nip05} qr />
            )}
            {profile?.lud16 && (
              <IdentityRow label="Lightning Address" value={profile.lud16} qr />
            )}

            {/* Operator-facing detail (hex/nprofile/LN URI) — collapsed by
                default, since it's not what a visitor scans this page for
                first. */}
            {nodeIdentity.hex && (
              <Accordion type="single" collapsible>
                <AccordionItem value="node-details" className="border-b-0">
                  <AccordionTrigger className="py-0 text-sm font-medium text-muted-foreground">
                    Node Details
                  </AccordionTrigger>
                  <AccordionContent className="pt-3">
                    <div className="grid gap-3">
                      <NodeIdentityRows identity={nodeIdentity} />
                      {uri && <IdentityRow label="LN URI" value={uri} />}
                    </div>
                  </AccordionContent>
                </AccordionItem>
              </Accordion>
            )}
          </div>
        </>
      ) : (
        <form onSubmit={handleSave} className="grid gap-4 px-4">
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
            <Label htmlFor="profile-banner">Banner URL</Label>
            <Input
              id="profile-banner"
              type="url"
              value={form.banner}
              onChange={(e) => setForm({ ...form, banner: e.target.value })}
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

      {/* Notes — this identity's own kind:1 history, newest first. A Load
          More button rather than infinite scroll, matching this app's
          plain-button pagination conventions elsewhere. */}
      <div className="grid gap-3 px-4">
        <Label>Notes</Label>
        {isNotesLoading && (
          <div className="grid gap-2">
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-16 w-full" />
          </div>
        )}
        {!isNotesLoading && notes.length === 0 && (
          <p className="text-sm text-muted-foreground">No notes yet.</p>
        )}
        {notes.map((note) => (
          <Card key={note.id}>
            <CardContent className="py-3">
              <p className="text-sm whitespace-pre-wrap break-words">
                {note.content}
              </p>
              <p className="mt-2 text-xs text-muted-foreground">
                {dayjs.unix(note.createdAt).fromNow()}
              </p>
            </CardContent>
          </Card>
        ))}
        {!isNotesLoading && !isLoadingMoreNotes && hasMoreNotes && (
          <Button variant="outline" className="w-fit" onClick={loadMoreNotes}>
            Load More
          </Button>
        )}
      </div>
    </div>
  );
}
