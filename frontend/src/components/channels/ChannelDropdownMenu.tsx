import {
    ExternalLinkIcon,
    HandCoinsIcon,
    MoreHorizontalIcon,
    Trash2Icon
} from "lucide-react";
import React from "react";
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { CloseChannelDialogContent } from "src/components/CloseChannelDialogContent";
import ExternalLink from "src/components/ExternalLink";

import { RoutingFeeDialogContent } from "src/components/RoutingFeeDialogContent";
import {
    AlertDialog,
    AlertDialogTrigger,
} from "src/components/ui/alert-dialog";
import { Button } from "src/components/ui/button.tsx";
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuTrigger,
} from "src/components/ui/dropdown-menu.tsx";
import { useInfo } from "src/hooks/useInfo";
import { Channel } from "src/types";

type ChannelDropdownMenuProps = {
  alias: string;
  channel: Channel;

};

export function ChannelDropdownMenu({
  alias,
  channel,
}: ChannelDropdownMenuProps) {
  const { data: info } = useInfo();
  const [searchParams] = useSearchParams();
  const { t } = useTranslation("channels");
  const [dialog, setDialog] = React.useState<
    "closeChannel" | "routingFee"
  >();

  React.useEffect(() => {
    // when opening the swap dialog, close existing dialog
    if (searchParams.has("swap", "true")) {
      setDialog(undefined);
    }
  }, [searchParams]);

  return (
    <AlertDialog
      open={!!dialog}
      onOpenChange={(open) => {
        if (!open) {
          setDialog(undefined);
        }
      }}
    >
      <DropdownMenu modal={false}>
        <Button asChild size="icon" variant="ghost">
          <DropdownMenuTrigger>
            <MoreHorizontalIcon />
          </DropdownMenuTrigger>
        </Button>
        <DropdownMenuContent align="end" className="w-64">

          <DropdownMenuItem>
            <ExternalLink
              className="flex flex-1 flex-row items-center gap-2"
              to={`${info?.mempoolUrl}/tx/${channel.fundingTxId}#flow=&vout=${channel.fundingTxVout}`}
            >
              <ExternalLinkIcon />
              {t("channels.dropdown.viewFunding", "View Funding Transaction")}
            </ExternalLink>
          </DropdownMenuItem>
          <DropdownMenuItem>
            <ExternalLink
              className="flex flex-1 flex-row items-center gap-2"
              to={`${info?.mempoolUrl}/lightning/node/${channel.remotePubkey}`}
            >
              <ExternalLinkIcon />
              {t("channels.dropdown.viewNode", "View Node on {{explorer}}", { explorer: info?.mempoolUrl ? new URL(info.mempoolUrl).hostname : "Explorer" })}
            </ExternalLink>
          </DropdownMenuItem>
          {channel.public && (
            <AlertDialogTrigger asChild>
              <DropdownMenuItem onClick={() => setDialog("routingFee")}>
                <HandCoinsIcon />
                {t("channels.dropdown.setRoutingFee", "Set Routing Fee")}
              </DropdownMenuItem>
            </AlertDialogTrigger>
          )}
          <AlertDialogTrigger asChild>
            <DropdownMenuItem onClick={() => setDialog("closeChannel")}>
              <Trash2Icon className="text-destructive" />
              {t("channels.close", "Close Channel")}
            </DropdownMenuItem>
          </AlertDialogTrigger>
        </DropdownMenuContent>
      </DropdownMenu>
      {dialog === "closeChannel" && (
        <CloseChannelDialogContent alias={alias} channel={channel} />
      )}
      {dialog === "routingFee" && <RoutingFeeDialogContent channel={channel} />}

    </AlertDialog>
  );
}
