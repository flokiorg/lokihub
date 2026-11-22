import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import { CircleUserRound } from "lucide-react";
import React, { useState } from "react";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import { IdentityRow, NodeIdentityRows } from "src/components/NodeIdentityRows";
import { NostrAvatar } from "src/components/NostrAvatar";
import { RelayHint } from "src/components/RelayHint";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "src/components/ui/accordion";
import { Avatar, AvatarFallback } from "src/components/ui/avatar";
import { Button } from "src/components/ui/button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { Skeleton } from "src/components/ui/skeleton";
import { Textarea } from "src/components/ui/textarea";
import { useImageProxySrc } from "src/hooks/useImageProxySrc";
import { useNodeConnectionInfo } from "src/hooks/useNodeConnectionInfo";
import { useNodeIdentity } from "src/hooks/useNodeIdentity";
import { useNostrNotes } from "src/hooks/useNostrNotes";
import { useNostrProfile } from "src/hooks/useNostrProfile";
import { cn } from "src/lib/utils";
import { handleRequestError } from "src/utils/handleRequestError";
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

// A bordered panel with a quiet uppercase heading — the recurring section
// container on this page so Profile / Identity / Notes each read as a block.
function Section({
  title,
  children,
  className,
}: {
  title?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("rounded-xl border bg-card p-4", className)}>
      {title && (
        <h3 className="mb-3 text-sm font-semibold uppercase tracking-wide text-muted-foreground">
          {title}
        </h3>
      )}
      {children}
    </section>
  );
}

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
    addNote,
  } = useNostrNotes(nodeIdentity.nostrHex);

  // The banner is a remote image URL like an avatar, so it goes through the
  // same image-proxy gating (P0-B): "off" (default) shows no banner, "direct"
  // loads the source, "proxy" routes it through the user's template.
  const bannerSrc = useImageProxySrc(profile?.banner);

  const [isEditing, setIsEditing] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const [form, setForm] = useState<ProfileForm>(emptyForm);
  const [noteDraft, setNoteDraft] = useState("");
  const [isPosting, setIsPosting] = useState(false);

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

      // Relay propagation/outbox indexing isn't instant — show what was just
      // submitted immediately and let the next natural revalidation pick up
      // whatever relays actually indexed.
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

  async function handlePostNote(e: React.FormEvent) {
    e.preventDefault();
    const content = noteDraft.trim();
    if (!content) {
      return;
    }
    setIsPosting(true);
    try {
      const response = await request<{ id: string; createdAt: number }>(
        "/api/node/note",
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ content }),
        }
      );
      if (response) {
        addNote({
          id: response.id,
          content,
          createdAt: response.createdAt,
        });
      }
      setNoteDraft("");
      toast("Note published.");
    } catch (error) {
      handleRequestError("Failed to publish note", error);
    } finally {
      setIsPosting(false);
    }
  }

  const displayName = profile?.name || profile?.displayName;

  return (
    <div className="grid max-w-2xl gap-5">
      <AppHeader
        title="Profile"
        description="Your node's Nostr identity and published profile"
      />

      <RelayHint />

      {!isEditing ? (
        <>
          {/* Profile card. A cover band shows only when a real banner image is
              set — an empty gradient just reads as unfinished — otherwise the
              avatar sits inline in a clean identity row. */}
          <div className="overflow-hidden rounded-xl border bg-card">
            {bannerSrc && (
              <div
                className="h-28 w-full bg-cover bg-center"
                style={{ backgroundImage: `url(${bannerSrc})` }}
              />
            )}
            <div
              className={cn(
                "flex items-start gap-4 p-4",
                bannerSrc && "-mt-12"
              )}
            >
              {isProfileLoading ? (
                <Skeleton className="size-16 rounded-full border-4 border-card" />
              ) : hasProfile ? (
                <NostrAvatar
                  pubkey={nodeIdentity.nostrHex ?? ""}
                  profile={profile}
                  className="size-16 border-4 border-card"
                />
              ) : (
                // Not NostrAvatar's npub-initials fallback: that is also what a
                // named-but-pictureless profile shows, so it wouldn't
                // distinguish "nothing published" from "published but sparse".
                <Avatar className="size-16 border-4 border-card">
                  <AvatarFallback>
                    <CircleUserRound className="size-8 text-muted-foreground" />
                  </AvatarFallback>
                </Avatar>
              )}
              <div className="min-w-0 flex-1 pt-1">
                {!isProfileLoading && (
                  <>
                    <div className="truncate text-lg font-semibold">
                      {displayName || "Your node"}
                    </div>
                    <p className="truncate text-sm text-muted-foreground">
                      {hasProfile
                        ? profile?.nip05 || "Profile published"
                        : "No profile published yet"}
                    </p>
                  </>
                )}
              </div>
              <Button
                onClick={startEditing}
                size="sm"
                className="mt-1 shrink-0"
              >
                {hasProfile ? "Edit profile" : "Create profile"}
              </Button>
            </div>
            {!isProfileLoading && hasProfile && profile?.about && (
              <p className="whitespace-pre-wrap px-4 pb-4 text-sm">
                {profile.about}
              </p>
            )}
          </div>

          {/* Identity — npub always, nip05/lud16 once published, each with a QR
              action. Operator-facing detail (hex/nprofile/LN URI) stays
              collapsed. */}
          <Section title="Identity">
            <div className="grid gap-3">
              {nodeIdentity.npub && (
                <IdentityRow label="Nostr npub" value={nodeIdentity.npub} qr />
              )}
              {profile?.nip05 && (
                <IdentityRow label="NIP-05" value={profile.nip05} qr />
              )}
              {profile?.lud16 && (
                <IdentityRow
                  label="Lightning Address"
                  value={profile.lud16}
                  qr
                />
              )}
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
          </Section>
        </>
      ) : (
        <Section>
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
        </Section>
      )}

      {/* Notes — this identity's own kind:1 history, newest first. */}
      <Section title="Notes">
        <div className="grid gap-3">
          <form onSubmit={handlePostNote} className="grid gap-2">
            <Textarea
              placeholder="Post a note..."
              value={noteDraft}
              onChange={(e) => setNoteDraft(e.target.value)}
              disabled={isPosting}
            />
            <Button
              type="submit"
              size="sm"
              className="w-fit"
              disabled={isPosting || !noteDraft.trim()}
            >
              {isPosting ? "Posting..." : "Post"}
            </Button>
          </form>
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
            <div
              key={note.id}
              className="border-b pb-3 last:border-b-0 last:pb-0"
            >
              <p className="whitespace-pre-wrap break-words text-sm">
                {note.content}
              </p>
              <p className="mt-1 text-xs text-muted-foreground">
                {dayjs.unix(note.createdAt).fromNow()}
              </p>
            </div>
          ))}
          {!isNotesLoading && !isLoadingMoreNotes && hasMoreNotes && (
            <Button
              variant="outline"
              size="sm"
              className="w-fit"
              onClick={loadMoreNotes}
            >
              Load More
            </Button>
          )}
        </div>
      </Section>
    </div>
  );
}
