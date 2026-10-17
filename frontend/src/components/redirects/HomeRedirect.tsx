import React from "react";
import { useLocation, useNavigate } from "react-router-dom";
import Loading from "src/components/Loading";
import { localStorageKeys } from "src/constants";
import { safeReturnPath } from "src/utils/safeReturnPath";
import { useInfo } from "src/hooks/useInfo";

export function HomeRedirect() {
  const { data: info } = useInfo();
  const location = useLocation();
  const navigate = useNavigate();

  React.useEffect(() => {
    if (!info) {
      return;
    }

    const setupReturnTo = safeReturnPath(
      window.localStorage.getItem(localStorageKeys.setupReturnTo)
    );

    let to: string;
    window.localStorage.removeItem(localStorageKeys.setupReturnTo);
    if (setupReturnTo) {
      to = setupReturnTo;
    } else if (info.setupCompleted && info.running) {
      if (info.unlocked) {
        const returnTo = safeReturnPath(
          window.localStorage.getItem(localStorageKeys.returnTo)
        );
        window.localStorage.removeItem(localStorageKeys.returnTo);
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
