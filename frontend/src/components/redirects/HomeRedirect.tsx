import React from "react";
import { useLocation, useNavigate } from "react-router-dom";
import Loading from "src/components/Loading";
import { localStorageKeys } from "src/constants";
import { useInfo } from "src/hooks/useInfo";

export function HomeRedirect() {
  const { data: info } = useInfo();
  const location = useLocation();
  const navigate = useNavigate();

  React.useEffect(() => {
    if (!info) {
      return;
    }

    const setupReturnTo = window.localStorage.getItem(
      localStorageKeys.setupReturnTo
    );

    let to: string;
    if (setupReturnTo) {
      to = setupReturnTo;
      window.localStorage.removeItem(localStorageKeys.setupReturnTo);
    } else if (info.setupCompleted && info.running) {
      if (info.unlocked) {
        const returnTo = window.localStorage.getItem(localStorageKeys.returnTo);
        if (returnTo) {
          window.localStorage.removeItem(localStorageKeys.returnTo);
        }
        to = returnTo || "/home";
      } else {
        to = "/unlock";
      }
    } else if (info.setupCompleted && !info.running) {
      to = "/start";
    } else {
      to = "/intro";
    }

    navigate(to, {
      replace: true,
    });
  }, [info, location, navigate]);

  return <Loading />;
}
