import React, { useState } from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import AppHeader from "src/components/AppHeader";
import { buttonVariants } from "../ui/buttonVariants";
import { useInfo } from "src/hooks/useInfo";
import { PowerIcon } from "lucide-react";
import { toast } from "sonner";
import {
    AlertDialog,
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
    AlertDialogTrigger,
} from "src/components/ui/alert-dialog";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Separator } from "src/components/ui/separator";
import { cn } from "src/lib/utils";
import { request } from "src/utils/request";

export default function SettingsLayout() {
  const {
    data: info,
    mutate: refetchInfo,
    hasMnemonic,
    hasNodeBackup,
  } = useInfo();
  const navigate = useNavigate();
  const { t } = useTranslation("settings");
  const [shuttingDown, setShuttingDown] = useState(false);

  const shutdown = React.useCallback(async () => {
    setShuttingDown(true);
    try {
      await request("/api/stop", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
      });

      await refetchInfo();
      setShuttingDown(false);
      navigate("/", { replace: true });
      toast(t("shutdown.toast"));
    } catch (error) {
      console.error(error);
      toast.error(t("shutdown.failedToast"), {
        description: "" + error,
      });
    }
  }, [navigate, refetchInfo]);

  return (
    <>
      <AppHeader
        title={t("header.title")}
        breadcrumb={false}
        contentRight={
          <div className="flex items-center gap-4">
            <div className="font-medium slashed-zero text-muted-foreground text-sm">
              {info?.version}
            </div>
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <LoadingButton
                  variant="destructive"
                  size="icon"
                  loading={shuttingDown}
                >
                  {!shuttingDown && <PowerIcon className="size-4" />}
                </LoadingButton>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>
                    {t("shutdown.dialogTitle")}
                  </AlertDialogTitle>
                  <AlertDialogDescription>
                    {t("shutdown.dialogDesc")}
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>{t("shutdown.cancel")}</AlertDialogCancel>
                  <AlertDialogAction onClick={shutdown}>
                    {t("shutdown.confirm")}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
        }
      />

      <div className="flex flex-col space-y-8 lg:flex-row lg:space-x-4 lg:space-y-0 h-full">
        <aside className="flex flex-col justify-between lg:w-1/5">
          <nav className="flex flex-wrap lg:flex-col lg:space-y-1">
            <MenuItem to="/settings">{t("nav.general")}</MenuItem>
            <MenuItem to="/settings/services">{t("nav.services")}</MenuItem>
            {info?.autoUnlockPasswordSupported && (
              <MenuItem to="/settings/auto-unlock">{t("nav.autoUnlock")}</MenuItem>
            )}
            <MenuItem to="/settings/change-unlock-password">
              {t("nav.unlockPassword")}
            </MenuItem>
            {hasMnemonic && <MenuItem to="/settings/backup">{t("nav.backup")}</MenuItem>}
            {hasNodeBackup && (
              <MenuItem to="/settings/node-migrate">{t("nav.migrate")}</MenuItem>
            )}
            <MenuItem to="/settings/developer">{t("nav.developer")}</MenuItem>
            <MenuItem to="/settings/debug-tools">{t("nav.debugTools")}</MenuItem>
            <MenuItem to="/settings/about">{t("nav.about")}</MenuItem>
          </nav>
        </aside>
        <Separator orientation="vertical" className="hidden lg:block" />
        <div className="flex-1 lg:max-w-2xl">
          <div className="grid gap-6">
            <Outlet />
          </div>
        </div>
      </div>
    </>
  );
}

const MenuItem = ({
  to,
  children,
}: {
  to: string;
  children: React.ReactNode | string;
}) => (
  <>
    <NavLink
      end
      to={to}
      className={({ isActive }) =>
        cn(
          buttonVariants({ variant: "ghost" }),
          isActive
            ? "bg-muted hover:bg-muted"
            : "hover:bg-transparent hover:underline",
          "justify-start"
        )
      }
    >
      {children}
    </NavLink>
  </>
);

MenuItem;
