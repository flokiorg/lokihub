import React from "react";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import Loading from "src/components/Loading";
import { useInfo } from "src/hooks/useInfo";
import useSetupStore from "src/state/SetupStore";

export function SetupRedirect() {
  const { data: info } = useInfo();
  const location = useLocation();
  const navigate = useNavigate();
  const store = useSetupStore();

  React.useEffect(() => {
    if (!info) {
      return;
    }
    // If node is already running and setup, redirect to home
    if (info.setupCompleted && info.running && location.pathname !== "/setup/security" && location.pathname !== "/setup/finish") {
      navigate("/");
      return;
    }

    // If we're not on the password screen, ensure the password is set in the store.
    // This handles page refreshing (which clears the store) and ensures state consistency.
    // If lost, redirect back to password creation/entry.
    if (location.pathname !== "/setup/password" && !store.unlockPassword) {
      navigate("/setup/password");
      return;
    }

  }, [info, location, navigate, store.unlockPassword]);

  if (!info) {
    return <Loading />;
  }

  return <Outlet />;
}
