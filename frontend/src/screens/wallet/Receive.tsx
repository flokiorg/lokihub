import React from "react";
import { useNavigate } from "react-router-dom";
import Loading from "src/components/Loading";

export default function Receive() {
  const navigate = useNavigate();

  React.useEffect(() => {
    navigate("/wallet/receive/invoice", { replace: true });
  }, [navigate]);

  return <Loading />;
}
