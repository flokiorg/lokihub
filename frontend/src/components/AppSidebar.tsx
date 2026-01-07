import {
  BoxIcon,
  CircleHelp,
  HomeIcon,
  LogOut,
  LucideIcon,
  Plug2Icon,
  Settings,
  SquareStack,
  WalletIcon,
} from "lucide-react";
import React from "react";

import { Link, NavLink, useLocation, useNavigate } from "react-router-dom";


import { LokihubLogo } from "src/components/icons/LokihubLogo";
import SidebarHint from "src/components/SidebarHint";

import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar
} from "src/components/ui/sidebar";

import { useHealthCheck } from "src/hooks/useHealthCheck";
import { useInfo } from "src/hooks/useInfo";
import { deleteAuthToken } from "src/lib/auth";
import { isHttpMode } from "src/utils/isHttpMode";
import { request } from "src/utils/request";

export function AppSidebar() {
  const { mutate: refetchInfo } = useInfo();
  const { hasChannelManagement } = useInfo();
  const navigate = useNavigate();
  const location = useLocation();
  const { setOpenMobile } = useSidebar();

  const _isHttpMode = isHttpMode();

  const logout = React.useCallback(async () => {
    deleteAuthToken();
    if (_isHttpMode) {
      window.location.href = "/logout";
    } else {
      try {
        await request("/api/stop", {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
          },
        });
      } catch (error) {
        console.error("Failed to stop node", error);
      }
      await refetchInfo();
      navigate("/", { replace: true });
    }
  }, [_isHttpMode, navigate, refetchInfo]);

  const data = {
    navMain: [
      {
        title: "Home",
        url: "/home",
        icon: HomeIcon,
      },
      {
        title: "Wallet",
        url: "/wallet",
        icon: WalletIcon,
      },
      {
        title: "Sub-wallets",
        url: "/sub-wallets",
        icon: SquareStack,
      },
      {
        title: "Connections",
        url: "/apps",
        icon: Plug2Icon,
      },
    ],
    navSecondary: [
      ...(hasChannelManagement
        ? [
            {
              title: "Node",
              url: "/channels",
              icon: BoxIcon,
            },
          ]
        : []),
      {
        title: "Settings",
        url: "/settings",
        icon: Settings,
      },
    ],
  };

  return (
    <Sidebar
      className="top-(--header-height) h-[calc(100svh-var(--header-height))]!"
      collapsible="offcanvas"
    >
      <SidebarHeader>
        <div className="p-2 flex flex-row items-center justify-between">
          <Link to="/home" onClick={() => setOpenMobile(false)}>
            <LokihubLogo className="w-32" />
          </Link>
          <div className="flex gap-3 items-center">
            <HealthIndicator />
          </div>
        </div>
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {data.navMain.map((item) => (
                <SidebarMenuItem key={item.title}>
                  <SidebarMenuButton
                    asChild
                    isActive={location.pathname === item.url}
                  >
                    <Link
                      to={item.url}
                      onClick={() => {
                        setOpenMobile(false);
                      }}
                    >
                      <item.icon />
                      <span>{item.title}</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        <div className="mt-auto">
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarHint />
            </SidebarGroupContent>
          </SidebarGroup>
          <NavSecondary items={data.navSecondary} logout={logout} />
        </div>
      </SidebarContent>
    </Sidebar>
  );
}

export function NavSecondary({
  items,
  logout,
  ...props
}: {
  items: {
    title: string;
    url: string;
    icon: LucideIcon;
  }[];
  logout: () => void;
} & React.ComponentPropsWithoutRef<typeof SidebarGroup>) {
  const { setOpenMobile } = useSidebar();
  const location = useLocation();

  return (
    <SidebarGroup {...props}>
      <SidebarGroupContent>
        <SidebarMenu>
          {items.map((item) => (
            <SidebarMenuItem key={item.title}>
              <SidebarMenuButton
                asChild
                isActive={location.pathname === item.url}
              >
                <NavLink
                  to={item.url}
                  end
                  onClick={() => {
                    setOpenMobile(false);
                  }}
                >
                  <item.icon />
                  <span>{item.title}</span>
                </NavLink>
              </SidebarMenuButton>
            </SidebarMenuItem>
          ))}
          <SidebarMenuItem>
            <SidebarMenuButton asChild>
              <Link to="/help" onClick={() => setOpenMobile(false)}>
                <CircleHelp className="h-4 w-4" />
                <span>Help</span>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton onClick={logout}>
              <LogOut />
              <span>Log out</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  );
}

function HealthIndicator() {
  const { data: health } = useHealthCheck();
  const { setOpenMobile } = useSidebar();

  if (!health) {
    return null;
  }

  const ok = !health.alarms?.length && !health.message;
  if (ok) {
    return null;
  }

  return (
    <Link to="/channels" onClick={() => setOpenMobile(false)}>
      <div className="w-2 h-2 rounded-full bg-destructive" />
    </Link>
  );
}
