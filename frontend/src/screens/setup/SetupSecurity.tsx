import {
  HandCoinsIcon,
  ShieldAlertIcon,
  UnlockIcon
} from "lucide-react";
import React, { useState } from "react";
import { useNavigate } from "react-router-dom";
import Loading from "src/components/Loading";
import { SetupLayout } from "./SetupLayout";

import TwoColumnLayoutHeader from "src/components/TwoColumnLayoutHeader";
import { Button } from "src/components/ui/button";
import { Checkbox } from "src/components/ui/checkbox";
import { Label } from "src/components/ui/label";

import { useInfo } from "src/hooks/useInfo";

export function SetupSecurity() {
  const navigate = useNavigate();
  const { data: info, isLoading } = useInfo();
  const [hasConfirmed, setConfirmed] = useState<boolean>(false);

  function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (isLoading) return; // Prevent action while loading
    
    if (info?.setupCompleted) {
      navigate("/");
    } else {
        // If setup is logically complete (we are here), but info says no, 
        // force a hard reload to sync state.
        window.location.href = "/";
    }
  }

  return (
    <SetupLayout>
      <div className="grid max-w-sm w-full">
        <form onSubmit={onSubmit} className="flex flex-col items-center w-full">
          <TwoColumnLayoutHeader
            title="Security & Recovery"
            description="Take your time to understand how to secure and recover your funds on Lokihub."
          />

          <div className="flex flex-col gap-6 w-full mt-6">
            <div className="flex gap-3 items-center">
              <div className="shrink-0">
                <HandCoinsIcon className="size-6" />
              </div>
              <span className="text-sm text-muted-foreground">
                Lokihub is a spending wallet - do not keep all your savings
                on it!
              </span>
            </div>
            <div className="flex gap-3 items-center">
              <div className="shrink-0">
                <UnlockIcon className="size-6" />
              </div>
              <span className="text-sm text-muted-foreground">
                Access to your Lokihub is protected by an unlock password you
                set. It cannot be recovered or reset.
              </span>
            </div>
            <div className="flex gap-3 items-center">
              <div className="shrink-0">
                <ShieldAlertIcon className="size-6" />
              </div>
              <span className="text-sm text-muted-foreground">
                Channel backups{" "}
                <span className="underline">are not handled</span> by Loki
                Hub. Please take care of your own backups.
              </span>
            </div>
            <div className="flex items-center">
              <Checkbox
                id="securePassword"
                required
                onCheckedChange={() => setConfirmed(!hasConfirmed)}
              />
              <Label
                htmlFor="securePassword"
                className="ml-2 text-foreground leading-4"
              >
                I understand how to secure and recover funds
              </Label>
            </div>
            <Button className="w-full" disabled={!hasConfirmed || isLoading} type="submit">
              {isLoading ? <Loading className="w-4 h-4 mr-2" /> : null}
              Continue
            </Button>
          </div>
        </form>
      </div>
    </SetupLayout>
  );
}
