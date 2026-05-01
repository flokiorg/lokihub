import React, { useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import useSetupStore from "src/state/SetupStore";
import { SetupLayout } from "./SetupLayout";

import { toast } from "sonner";
import PasswordInput from "src/components/password/PasswordInput";
import TwoColumnLayoutHeader from "src/components/TwoColumnLayoutHeader";
import { Button } from "src/components/ui/button";
import { Checkbox } from "src/components/ui/checkbox";
import { Label } from "src/components/ui/label";
import { useInfo } from "src/hooks/useInfo";

export function SetupPassword() {
  const navigate = useNavigate();
  const store = useSetupStore();
  const { data: info } = useInfo();
  const { t } = useTranslation("setup");
  const [confirmPassword, setConfirmPassword] = React.useState("");
  const [isPasswordSecured, setIsPasswordSecured] = useState<boolean>(false);
  const [isPasswordSecured2, setIsPasswordSecured2] = useState<boolean>(false);

  const [searchParams] = useSearchParams();
  const node = searchParams.get("node") || "";

  function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!info) {
      return;
    }
    if (!isPasswordSecured || !isPasswordSecured2) {
      toast.error(t("password.notConfirmed"));
      return;
    }
    if (store.unlockPassword !== confirmPassword) {
      toast.error(t("password.mismatch"));
      return;
    }

    if (node) {
      navigate(`/setup/node/${node}`);
    } else {
      navigate(`/setup/services`);
    }
  }

  return (
    <SetupLayout>
      <form onSubmit={onSubmit} className="flex flex-col items-center w-full">
        <div className="grid gap-4 w-full">
          <TwoColumnLayoutHeader
            title={t("password.title")}
            description={t("password.description")}
          />
          <div className="grid gap-4 w-full">
            <div className="grid gap-1.5">
              <Label htmlFor="unlock-password">{t("password.label")}</Label>
              <PasswordInput
                id="unlock-password"
                onChange={store.setUnlockPassword}
                autoComplete="new-password"
                autoFocus
                value={store.unlockPassword}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="confirm-password">{t("password.repeatLabel")}</Label>
              <PasswordInput
                id="confirm-password"
                autoComplete="new-password"
                placeholder={t("password.repeatPlaceholder")}
                onChange={setConfirmPassword}
                value={confirmPassword}
              />
            </div>
          </div>
          <div className="grid gap-6">
            <div className="flex items-center">
              <Checkbox
                id="securePassword"
                required
                onCheckedChange={() =>
                  setIsPasswordSecured(!isPasswordSecured)
                }
              />
              <Label
                htmlFor="securePassword"
                className="ml-2 text-foreground leading-4"
              >
                {t("password.securedConfirm")}
              </Label>
            </div>
            {isPasswordSecured && (
              <div className="flex items-center">
                <Checkbox
                  id="securePassword2"
                  required
                  onCheckedChange={() =>
                    setIsPasswordSecured2(!isPasswordSecured2)
                  }
                />
                <Label
                  htmlFor="securePassword2"
                  className="ml-2 leading-4 font-semibold"
                >
                  {t("password.irrecoverableConfirm")}
                </Label>
              </div>
            )}
            <Button type="submit">{t("password.createButton")}</Button>
          </div>
        </div>
      </form>
    </SetupLayout>
  );
}
