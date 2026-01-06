import React, { useState } from "react";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import { Button } from "src/components/ui/button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { useInfo } from "src/hooks/useInfo";

import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

export default function NodeAlias() {
  const { data: info, mutate: reloadInfo } = useInfo();

  const [nodeAlias, setNodeAlias] = useState("");
  const [isLoading, setIsLoading] = useState(false);

  // Initialize nodeAlias with current value when info loads
  React.useEffect(() => {
    if (info?.nodeAlias !== undefined) {
      setNodeAlias(info.nodeAlias);
    }
  }, [info?.nodeAlias]);



  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();



    setIsLoading(true);
    try {
      await request("/api/node/alias", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ nodeAlias }),
      });

      await reloadInfo();
      toast("Alias changed. Restart your node to apply the change.", {
        description: "Your node alias has been updated successfully.",
      });
    } catch (error) {
      console.error("Failed to update node alias:", error);
      handleRequestError("Failed to update node alias", error);
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <div className="grid gap-5">
      <AppHeader
        title="Node Alias"
        description="Set a human-readable name for your lightning node"
      />
      <div className="max-w-lg">
        <form onSubmit={handleSubmit} className="w-full flex flex-col gap-4">
          <div className="grid gap-2">
            <Label htmlFor="nodeAlias">Node Alias</Label>
            <Input
              id="nodeAlias"
              type="text"
              value={nodeAlias}
              onChange={(e) => setNodeAlias(e.target.value)}
              placeholder="myLokinode"
              className="w-full md:w-60"
            />
            <p className="text-sm text-muted-foreground">
              Your lightning node alias will appear to your channel partners,
              connected peers, and on lightning network explorers such as
              flokichain.info.
            </p>
          </div>
          <Button type="submit" disabled={isLoading} className="w-fit">
            {isLoading ? "Updating..." : "Update Alias"}
          </Button>
        </form>
      </div>
    </div>
  );
}
