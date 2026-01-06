
import {
    AlertTriangle,
    EyeIcon
} from "lucide-react";
import React, { useState } from "react";

import MnemonicDialog from "src/components/mnemonic/MnemonicDialog";
import PasswordInput from "src/components/password/PasswordInput";
import SettingsHeader from "src/components/SettingsHeader";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { Badge } from "src/components/ui/badge";
import { Checkbox } from "src/components/ui/checkbox";

import { toast } from "sonner";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Label } from "src/components/ui/label";
import { Separator } from "src/components/ui/separator";
import { useInfo } from "src/hooks/useInfo";
import { MnemonicResponse } from "src/types";
import { request } from "src/utils/request";

export default function Backup() {
  const { hasMnemonic } = useInfo();
  const [unlockPassword, setUnlockPassword] = useState("");
  const [decryptedMnemonic, setDecryptedMnemonic] = useState("");
  const [loading, setLoading] = useState(false);
  const [isDialogOpen, setIsDialogOpen] = useState(false);

  const onSubmitPassword = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      setLoading(true);
      const result = await request<MnemonicResponse>("/api/mnemonic", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ unlockPassword }),
      });

      setDecryptedMnemonic(result?.mnemonic ?? "");
      setIsDialogOpen(true);
    } catch (error) {
      toast.error("Incorrect password", {
        description: "Failed to decrypt recovery phrase.",
      });
    } finally {
      setLoading(false);
    }
  };

  return (
    <>
      <SettingsHeader
        title="Backup"
        description={
          <>
            <span className="text-muted-foreground">
              Backup your recovery phrase and channel states. These backups are
              for disaster recovery only. To migrate your node, please use the
              migration tool.{" "}
            </span>
          </>
        }
      />

      {hasMnemonic && (
        <div className="flex flex-col gap-6">
          <div>
            <h3 className="text-lg font-medium">Recovery Phrase</h3>
            <p className="text-sm text-muted-foreground">
              Your recovery phrase is a group of 12 random words that back up
              your wallet on-chain balance. Using them is the only way to
              recover access to your wallet on another machine or when you lose
              your unlock password.
            </p>
          </div>
          <Alert variant="destructive">
            <AlertTriangle />
            <AlertTitle>Important</AlertTitle>
            <AlertDescription>
              If you lose access to your Hub and do not have your recovery
              phrase, you will lose access to your funds.
            </AlertDescription>
          </Alert>

          <div>
            <form
              onSubmit={onSubmitPassword}
              className="max-w-md flex flex-col gap-6"
            >
              <div className="grid gap-2">
                <Label htmlFor="password">Password</Label>
                <PasswordInput
                  id="password"
                  onChange={setUnlockPassword}
                  value={unlockPassword}
                />
                <p className="text-sm text-muted-foreground">
                  Enter your unlock password to view your recovery phrase.
                </p>
              </div>
              {!!unlockPassword && (
                <div className="flex">
                  <Checkbox id="private" required className="mt-0.5" />
                  <Label
                    htmlFor="private"
                    className="ml-2 text-sm text-foreground"
                  >
                    I'll NEVER share my recovery phrase with anyone, including
                    Loki support
                  </Label>
                </div>
              )}
              <div className="flex justify-start">
                <LoadingButton
                  loading={loading}
                  variant="secondary"
                  className="flex gap-2 justify-center"
                >
                  <EyeIcon />
                  View Recovery Phrase
                </LoadingButton>
              </div>
            </form>
          </div>
          <MnemonicDialog
            open={isDialogOpen}
            onOpenChange={setIsDialogOpen}
            mnemonic={decryptedMnemonic}
          />
        </div>
      )}
      <>
        <Separator className="my-2" />
        <div className="flex flex-col gap-8">
          <div>
            <h3 className="text-lg font-medium">Channels Backup</h3>
            <p className="text-sm text-muted-foreground">
              Your spending balance is stored in your lightning channels. In
              case of recovery of your Lokihub, they need to be backed up every
              time you open a new channel.
            </p>
          </div>

          <div>
                <div className="flex flex-col gap-1">
                  <div className="flex gap-2 items-center">
                    <h3 className="text-sm font-medium">
                      Manual Channels Backup
                    </h3>
                    <Badge variant={"positive"}>Active</Badge>
                  </div>
                </div>
          </div>
        </div>
      </>

      {!hasMnemonic && (
        <p className="text-sm text-muted-foreground">
          No recovery phrase or channel state backup present.
        </p>
      )}
    </>
  );
}
