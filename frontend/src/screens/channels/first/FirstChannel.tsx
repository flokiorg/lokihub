import React from "react";
import { useNavigate } from "react-router-dom";
import Loading from "src/components/Loading";

export function FirstChannel() {
  const navigate = useNavigate();

  React.useEffect(() => {
    navigate("/channels/outgoing", { replace: true });
  }, [navigate]);

  return <Loading />;
}
