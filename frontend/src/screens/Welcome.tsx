import React from "react";
import { useNavigate } from "react-router-dom";
import Container from "src/components/Container";
import { Button } from "src/components/ui/button";
import { localStorageKeys } from "src/constants";
import { useInfo } from "src/hooks/useInfo";

export function Welcome() {
  const { data: info } = useInfo();
  const navigate = useNavigate();

  React.useEffect(() => {
    if (!info?.setupCompleted) {
      return;
    }
    navigate("/");
  }, [info, navigate]);

  function navigateToAuthPage(returnTo: string) {
    window.localStorage.setItem(localStorageKeys.setupReturnTo, returnTo);
    navigate(returnTo);
  }

  return (
    <Container>
      <div className="grid text-center gap-5">
        <div className="grid gap-2">
          <h1 className="font-semibold text-2xl font-headline">
            Welcome to Lokihub
          </h1>
          <p className="text-muted-foreground">
            A powerful, all-in-one Flokicoin Lightning wallet powering the next web of engagement.
          </p>
        </div>
        <div className="grid gap-2">
          <Button
            className="w-full"
            onClick={() =>
              navigateToAuthPage(
                  info?.backendType
                    ? "/setup/password" // node already setup through env variables
                    : "/setup/password"
              )
            }
          >
            Get Started
            {info?.backendType && ` (${info?.backendType})`}
          </Button>


        </div>
      </div>
    </Container>
  );
}
