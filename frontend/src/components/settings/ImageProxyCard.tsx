import { ImageOff } from "lucide-react";
import { useEffect, useState } from "react";
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
import { Label } from "src/components/ui/label";
import { RadioGroup, RadioGroupItem } from "src/components/ui/radio-group";
import { useHubInfo } from "src/hooks/useHubInfo";
import { ImageProxyMode } from "src/types";
import { BUILTIN_IMAGE_PROXY_TEMPLATE } from "src/utils/imageProxy";
import { request } from "src/utils/request";

const MODES: { value: ImageProxyMode; title: string; description: string }[] = [
  {
    value: "off",
    title: "Off (recommended)",
    description:
      "Never load external profile images. Avatars show initials and the node banner stays blank. Nothing is requested from image hosts.",
  },
  {
    value: "direct",
    title: "Direct",
    description:
      "Load profile images straight from their host. Simple, but the host sees your IP address and which profiles you view.",
  },
  {
    value: "proxy",
    title: "Through a proxy",
    description:
      "Route profile images through a URL template you control, so no image host sees your IP directly.",
  },
];

// clientValidateTemplate mirrors the backend contract (config.ValidateImageProxyTemplate)
// enough to disable Save before a round-trip; the backend remains authoritative.
function templateError(template: string): string | null {
  const trimmed = template.trim();
  const urlCount = (trimmed.match(/\{url\}/g) || []).length;
  const b64Count = (trimmed.match(/\{b64url\}/g) || []).length;
  if (urlCount + b64Count !== 1) {
    return "Template must contain exactly one of {url} or {b64url}.";
  }
  const sample = trimmed
    .replace("{url}", "https%3A%2F%2Fexample.com%2Fa.png")
    .replace("{b64url}", "aHR0cHM6Ly9leGFtcGxlLmNvbS9hLnBuZw");
  if (sample.startsWith("/api/")) {
    return null;
  }
  if (!sample.startsWith("https://") && !sample.startsWith("http://")) {
    return "Template must be an http(s) URL or a same-origin /api/... path.";
  }
  return null;
}

export function ImageProxyCard({ className }: { className?: string }) {
  const { data: hubInfo, mutate } = useHubInfo();

  const [mode, setMode] = useState<ImageProxyMode>("off");
  const [template, setTemplate] = useState("");
  const [saving, setSaving] = useState(false);

  const savedMode = (hubInfo?.imageProxyMode ?? "off") as ImageProxyMode;
  const savedTemplate = hubInfo?.imageProxyTemplate ?? "";

  // Sync local state from the hub once it loads, and whenever the saved value
  // changes while there are no local edits.
  const dirty = mode !== savedMode || template !== savedTemplate;
  useEffect(() => {
    setMode(savedMode);
    setTemplate(savedTemplate);
    // Re-sync only when the saved values themselves change.
  }, [savedMode, savedTemplate]);

  const tplError = mode === "proxy" ? templateError(template) : null;
  const canSave = dirty && !saving && !tplError;

  async function save() {
    setSaving(true);
    try {
      await request("/api/settings", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          imageProxyMode: mode,
          imageProxyTemplate: template,
        }),
      });
      await mutate();
      toast.success("Image proxy updated");
    } catch (e) {
      toast.error("Failed to update image proxy", {
        description: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card className={className}>
      <CardHeader className="pb-3">
        <CardTitle className="text-lg flex items-center gap-2">
          <ImageOff className="w-5 h-5 text-primary" />
          Image proxy
        </CardTitle>
        <CardDescription>
          Decide whether and how external profile images (avatars and node
          banners) are loaded. Off by default, so no image host is contacted
          until you choose.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <RadioGroup
          value={mode}
          onValueChange={(value) => setMode(value as ImageProxyMode)}
        >
          {MODES.map((option) => (
            <label
              key={option.value}
              htmlFor={`image-proxy-${option.value}`}
              className="flex items-start gap-3 rounded-lg border border-border p-3 cursor-pointer"
            >
              <RadioGroupItem
                id={`image-proxy-${option.value}`}
                value={option.value}
                className="mt-0.5"
              />
              <div className="space-y-1">
                <p className="text-sm font-medium leading-none">
                  {option.title}
                </p>
                <p className="text-sm text-muted-foreground">
                  {option.description}
                </p>
              </div>
            </label>
          ))}
        </RadioGroup>

        {mode === "proxy" && (
          <div className="grid gap-2">
            <Label htmlFor="image-proxy-template">Proxy URL template</Label>
            <Input
              id="image-proxy-template"
              value={template}
              placeholder="https://wsrv.nl/?url={url}"
              onChange={(e) => setTemplate(e.target.value)}
              aria-invalid={!!tplError}
            />
            <p className="text-xs text-muted-foreground">
              Use <code>{"{url}"}</code> for the source image URL
              (percent-encoded) or <code>{"{b64url}"}</code> for its base64url
              form.
            </p>
            <div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setTemplate(BUILTIN_IMAGE_PROXY_TEMPLATE)}
                disabled={template === BUILTIN_IMAGE_PROXY_TEMPLATE}
              >
                Use built-in hardened proxy
              </Button>
            </div>
            {tplError && (
              <p className="text-xs text-destructive">{tplError}</p>
            )}
          </div>
        )}

        <div className="flex justify-end">
          <Button type="button" onClick={save} disabled={!canSave}>
            {saving ? "Saving..." : "Save"}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
