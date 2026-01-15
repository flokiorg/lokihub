import { Check, Droplet, Plus, Server, Trash2 } from "lucide-react";
import { useState } from "react";
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
import { LSP } from "src/hooks/useLSPSManagement";
import { centerTrim, cn } from "src/lib/utils";
import { validateLSPURI } from "src/utils/validation";

interface LSPManagementCardProps {
  localLSPs: LSP[];
  setLocalLSPs: (lsps: LSP[]) => void;
  className?: string;
  // Callback when validation fails (optional, to bubble up to parent)
  inputError?: (error: string) => void;
}

export function LSPManagementCard({
  localLSPs,
  setLocalLSPs,
  className,
}: LSPManagementCardProps) {
  const [newLSPName, setNewLSPName] = useState("");
  const [newLSPURI, setNewLSPURI] = useState("");
  const [isAddingLSP, setIsAddingLSP] = useState(false);

  const separateLSPs = () => {
    // Identify community LSPs by checking if they match any option in the list
    // OR if they have the isCommunity flag (if we set it elsewhere)
    // Here we rely on the `isCommunity` flag which should be set by the parent when merging
    // If parent doesn't set it, we might need to infer it. The current implementation in Services.tsx sets it.
    // Based on Services.tsx logic, `isCommunity` is set during merge.
    
    // Safety check: filter out invalid entries if any
    const validLSPs = localLSPs || [];
    const community = validLSPs.filter((l) => l.isCommunity);
    const custom = validLSPs.filter((l) => !l.isCommunity);
    return { community, custom };
  };

  const { community: communityCards, custom: customCards } = separateLSPs();

  const handleAddLocalLSP = () => {
    if (!newLSPName.trim()) {
      toast.error("LSP Name is required");
      return;
    }
    if (
      localLSPs.some(
        (l) => l.name.toLowerCase() === newLSPName.toLowerCase()
      )
    ) {
      toast.error("LSP Name must be unique");
      return;
    }

    if (!newLSPURI.trim()) {
      toast.error("LSP URI is required");
      return;
    }
    const uriErr = validateLSPURI(newLSPURI);
    if (uriErr) {
      toast.error(uriErr);
      return;
    }

    // Parse URI to get pubkey and host
    const parts = newLSPURI.split("@");
    if (parts.length !== 2) {
      toast.error("Invalid URI format");
      return;
    }
    const pubkey = parts[0];
    const host = parts[1];

    if (localLSPs.some((l) => l.pubkey === pubkey)) {
      toast.error("LSP with this Pubkey already exists");
      return;
    }

    const newLSP: LSP = {
      name: newLSPName,
      pubkey: pubkey,
      host: host,
      active: true,
      isCommunity: false,
    };

    setLocalLSPs([...localLSPs, newLSP]);
    setNewLSPName("");
    setNewLSPURI("");
    setIsAddingLSP(false);
  };

  const removeLocalLSP = (pubkey: string) => {
    setLocalLSPs(localLSPs.filter((l) => l.pubkey !== pubkey));
    // Provide immediate feedback about the need to save
    toast.info("LSP removed. Save changes to persist.");
  };

  const toggleLocalLSP = (pubkey: string, active: boolean) => {
    setLocalLSPs(
      localLSPs.map((l) => (l.pubkey === pubkey ? { ...l, active } : l))
    );
  };

  return (
    <Card className={className}>
      <CardHeader>
        <CardTitle className="text-base">Lightning Service Providers</CardTitle>
        <CardDescription>
          Manage LSPs for JIT channels and inbound liquidity.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-[repeat(auto-fit,minmax(320px,1fr))] gap-4">
          {/* Community LSPs */}
          {communityCards.map((provider) => (
            <div
              key={provider.pubkey}
              onClick={() => toggleLocalLSP(provider.pubkey, !provider.active)}
              className={cn(
                "relative group flex flex-col p-4 rounded-xl border transition-all duration-200 cursor-pointer select-none",
                "hover:shadow-md active:scale-[0.98]",
                provider.active
                  ? "border-primary bg-primary/5 shadow-sm"
                  : "border-border bg-card hover:border-primary/50"
              )}
            >
              <div className="flex items-start justify-between">
                <div className="flex items-center gap-2">
                  <div
                    className={cn(
                      "p-2 rounded-lg transition-colors",
                      provider.active
                        ? "bg-primary text-primary-foreground"
                        : "bg-muted text-muted-foreground"
                    )}
                  >
                    <Droplet className="w-4 h-4" />
                  </div>
                  <div className="flex flex-col">
                    <span className="font-semibold text-sm leading-none">
                      {provider.name}
                    </span>
                    <span className="text-[10px] text-muted-foreground font-medium mt-1">
                      {provider.active ? "Active" : "Inactive"}
                    </span>
                  </div>
                </div>
                {provider.active && (
                  <div className="bg-primary text-primary-foreground rounded-full p-0.5 shrink-0">
                    <Check className="w-3 h-3" />
                  </div>
                )}
              </div>

              {provider.description && (
                <p className="text-xs text-muted-foreground line-clamp-2 leading-snug mt-3">
                  {provider.description}
                </p>
              )}

              <div className="flex flex-col gap-1.5 mt-3 pt-3 border-t border-border/50">
                <div className="flex items-center justify-between gap-2">
                  <p
                    className="text-[10px] text-muted-foreground opacity-50 font-mono truncate cursor-help"
                    title={`${provider.pubkey}@${provider.host}`}
                  >
                    {centerTrim(provider.pubkey)}@{provider.host}
                  </p>
                </div>
              </div>
            </div>
          ))}

          {customCards.map((provider) => (
            <div
              key={provider.pubkey}
              onClick={(e) => {
                // Don't toggle if clicking delete
                if ((e.target as HTMLElement).closest("button")) return;
                toggleLocalLSP(provider.pubkey, !provider.active);
              }}
              className={cn(
                "relative group flex flex-col p-4 rounded-xl border transition-all duration-200 cursor-pointer select-none",
                "hover:shadow-md active:scale-[0.98]",
                provider.active
                  ? "border-primary bg-primary/5 shadow-sm"
                  : "border-border bg-card hover:border-primary/50"
              )}
            >
              <div className="flex items-start justify-between">
                <div className="flex items-center gap-2">
                  <div
                    className={cn(
                      "p-2 rounded-lg transition-colors",
                      provider.active
                        ? "bg-primary text-primary-foreground"
                        : "bg-muted text-muted-foreground"
                    )}
                  >
                    <Droplet className="w-4 h-4" />
                  </div>
                  <div className="flex flex-col">
                    <span className="font-semibold text-sm leading-none">
                      {provider.name}
                    </span>
                    <span className="text-[10px] text-muted-foreground font-medium mt-1">
                      {provider.active ? "Active" : "Inactive"}
                    </span>
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-6 w-6 text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                    onClick={(e) => {
                      e.stopPropagation();
                      removeLocalLSP(provider.pubkey);
                    }}
                  >
                    <Trash2 className="w-3 h-3" />
                  </Button>
                  {provider.active && (
                    <div className="bg-primary text-primary-foreground rounded-full p-0.5 shrink-0">
                      <Check className="w-3 h-3" />
                    </div>
                  )}
                </div>
              </div>

              <div className="flex flex-col gap-1.5 mt-3 pt-3 border-t border-border/50">
                <div className="flex items-center justify-between gap-2">
                  <p
                    className="text-[10px] text-muted-foreground opacity-50 font-mono truncate cursor-help"
                    title={`${provider.pubkey}@${provider.host}`}
                  >
                    {centerTrim(provider.pubkey)}@{provider.host}
                  </p>
                </div>
              </div>
            </div>
          ))}

          {/* Add New LSP Card */}
          <div
            className={cn(
              "relative flex flex-col p-4 rounded-xl border border-dashed border-border transition-all duration-200",
              isAddingLSP
                ? "bg-card shadow-md ring-1 ring-primary border-primary"
                : "bg-transparent hover:border-primary hover:shadow-sm cursor-pointer group"
            )}
            onClick={() => !isAddingLSP && setIsAddingLSP(true)}
          >
            {!isAddingLSP ? (
              <div className="flex flex-col items-center justify-center h-full py-6 text-muted-foreground group-hover:text-primary transition-colors">
                <div className="flex-shrink-0 w-12 h-12 flex items-center justify-center rounded-full bg-muted/50 mb-3 group-hover:bg-primary/10 group-hover:scale-110 transition-all duration-300">
                  <Plus className="w-5 h-5" />
                </div>
                <span className="font-medium text-sm">Add Custom LSP</span>
              </div>
            ) : (
              <div className="flex flex-col h-full animate-in fade-in zoom-in-95 duration-200">
                <div className="flex items-center justify-between mb-4">
                  <span className="font-semibold text-sm">New Service</span>
                  <Server className="w-4 h-4 text-muted-foreground" />
                </div>

                <div className="flex-1 space-y-3">
                  <div className="space-y-1">
                    <Label htmlFor="lsp-name" className="text-xs">
                      Name
                    </Label>
                    <Input
                      id="lsp-name"
                      value={newLSPName}
                      onChange={(e) => setNewLSPName(e.target.value)}
                      placeholder="My Node"
                      className="h-8 text-xs bg-background"
                      autoFocus
                      onKeyDown={(e) => {
                        if (e.key === "Enter") handleAddLocalLSP();
                        if (e.key === "Escape") {
                          setIsAddingLSP(false);
                          setNewLSPName("");
                          setNewLSPURI("");
                        }
                      }}
                    />
                  </div>
                  <div className="space-y-1">
                    <Label htmlFor="lsp-uri" className="text-xs">
                      URI (pubkey@host:port)
                    </Label>
                    <Input
                      id="lsp-uri"
                      value={newLSPURI}
                      onChange={(e) => {
                        setNewLSPURI(e.target.value);
                      }}
                      placeholder="02abc...@127.0.0.1:5521"
                      className={cn(
                        "h-8 text-xs bg-background font-mono",
                        newLSPURI &&
                          validateLSPURI(newLSPURI) &&
                          "border-destructive focus-visible:ring-destructive"
                      )}
                      onKeyDown={(e) => {
                        if (e.key === "Enter") handleAddLocalLSP();
                        if (e.key === "Escape") {
                          setIsAddingLSP(false);
                          setNewLSPName("");
                          setNewLSPURI("");
                        }
                      }}
                    />
                    {newLSPURI && validateLSPURI(newLSPURI) && (
                      <p className="text-[10px] text-destructive font-medium mt-1">
                        {validateLSPURI(newLSPURI)}
                      </p>
                    )}
                  </div>
                </div>

                <div className="flex items-center gap-2 mt-4 pt-2">
                  <Button
                    size="sm"
                    variant="outline"
                    className="flex-1 h-7 text-xs"
                    onClick={(e) => {
                      e.stopPropagation();
                      setIsAddingLSP(false);
                      setNewLSPName("");
                      setNewLSPURI("");
                    }}
                  >
                    Cancel
                  </Button>
                  <Button
                    size="sm"
                    className="flex-1 h-7 text-xs"
                    onClick={(e) => {
                      e.stopPropagation();
                      handleAddLocalLSP();
                    }}
                    disabled={
                      !newLSPName || !newLSPURI || !!validateLSPURI(newLSPURI)
                    }
                  >
                    Add
                  </Button>
                </div>
              </div>
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
