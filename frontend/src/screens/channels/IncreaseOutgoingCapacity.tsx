import { InfoIcon, Zap } from "lucide-react";
import React, { FormEvent, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import { ChannelPublicPrivateAlert } from "src/components/channels/ChannelPublicPrivateAlert";
import { DuplicateChannelAlert } from "src/components/channels/DuplicateChannelAlert";
import { SwapAlert } from "src/components/channels/SwapAlert";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import { MempoolAlert } from "src/components/MempoolAlert";
import { Alert, AlertDescription } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
import { Checkbox } from "src/components/ui/checkbox";
import { Input } from "src/components/ui/input";
import { CurrencyInput } from "src/components/CurrencyInput";
import { LinkButton } from "src/components/ui/custom/link-button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "src/components/ui/dialog";
import { Label } from "src/components/ui/label";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "src/components/ui/select";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "src/components/ui/tooltip";
import { useBalances } from "src/hooks/useBalances";
import { useChannels } from "src/hooks/useChannels";
import { useInfo } from "src/hooks/useInfo";
import { usePeers } from "src/hooks/usePeers";
import { useUnit } from "src/hooks/useUnit";
import { cn } from "src/lib/utils";
import useChannelOrderStore from "src/state/ChannelOrderStore";
import {
  Channel,
  Network,
  NewChannelOrder,
  OnchainOrder,
} from "src/types";

import LightningNetworkDark from "src/assets/illustrations/lightning-network-dark.svg?react";
import LightningNetworkLight from "src/assets/illustrations/lightning-network-light.svg?react";
import { useNodeDetails } from "src/hooks/useNodeDetails";



export default function IncreaseOutgoingCapacity() {
  const { data: info } = useInfo();
  const { data: channels } = useChannels();

  if (!info?.network || !channels) {
    return <Loading />;
  }

  return <NewChannelInternal network={info.network} channels={channels} />;
}

function NewChannelInternal({
  channels,
}: {
  network: Network;
  channels: Channel[];
}) {
  const { data: info } = useInfo();
  const { data: balances } = useBalances();
  const { t } = useTranslation("channels");
  const { t: tc } = useTranslation("common");
  const { displayFormat, parseInputAmount, scaleInputAmount } = useUnit();
  const navigate = useNavigate();

  const [inputUnit, setInputUnit] = React.useState<"FLC" | "loki">("FLC");
  React.useEffect(() => {
    if (displayFormat === "flc") setInputUnit("FLC");
    else if (displayFormat === "loki") setInputUnit("loki");
    else setInputUnit("FLC");
  }, [displayFormat]);

  const handleInputUnitChange = (newUnit: "FLC" | "loki") => {
    if (order.amount) {
      const amountLoki = parseInputAmount(parseFloat(order.amount), inputUnit);
      if (!isNaN(amountLoki)) {
        const newAmount = scaleInputAmount(amountLoki, newUnit);
        setAmount(newAmount.toString());
      }
    }
    setInputUnit(newUnit);
  };

  const flcPresets = [21, 55, 500];
  const lokiPresets = [21000, 500000, 21000000];

  const [order, setOrder] = React.useState<Partial<OnchainOrder>>({
    paymentMethod: "onchain",
    status: "pay",
    amount: "",
    isPublic: !!channels.length && channels.every((channel) => channel.public),
  });

  // Remove initial amount prefill useEffect

  const [showConfirmModal, setShowConfirmModal] = React.useState(false);

  function setPublic(isPublic: boolean) {
    setOrder((current) => ({
      ...current,
      isPublic,
    }));
  }

  const setAmount = React.useCallback((amount: string) => {
    setOrder((current) => ({
      ...current,
      amount,
    }));
  }, []);
  function onSubmit(e: FormEvent) {
    e.preventDefault();
    setShowConfirmModal(true);
  }

  function handleConfirmSubmit() {
    try {
      if (!channels) {
        throw new Error("Channels not loaded");
      }
      if (
        channels.some(
          (channel) =>
            channel.status === "opening" &&
            channel.isOutbound &&
            !channel.confirmations
        )
      ) {
        throw new Error(
          "You already are opening a channel which has not been confirmed yet. Please wait for one block confirmation."
        );
      }

      const amountLoki = parseInputAmount(parseFloat(order.amount || "0"), inputUnit);
      useChannelOrderStore.getState().setOrder({
        ...order,
        amount: amountLoki.toString(),
      } as NewChannelOrder);
      setShowConfirmModal(false);
      navigate("/channels/order");
    } catch (error) {
      toast.error("Something went wrong", {
        description: `${error}`,
      });
      setShowConfirmModal(false);
    }
  }

  if (!balances) {
    return <Loading />;
  }

  const openImmediately =
    order.amount &&
    order.paymentMethod === "onchain" &&
    parseInputAmount(parseFloat(order.amount), inputUnit) < balances.onchain.spendable;

  return (
    <>
      <AppHeader
        title={t("increaseCapacity.title", "Open Channel with On-Chain")}
        description={t("increaseCapacity.description", "Funds used to open a channel minus fees will be added to your spending balance")}
      />
      <div className="md:max-w-md max-w-full flex flex-col gap-5 flex-1">
        <LightningNetworkDark
          className="w-full hidden dark:block"
        />
        <LightningNetworkLight className="w-full dark:hidden" />
        <p className="text-muted-foreground">
          {t("increaseCapacity.info", "Open a channel with on-chain funds. Both parties are free to close the channel at any time. However, by keeping more funds on your side of the channel and using it regularly, there is more chance the channel will stay open.")}
        </p>

        <Alert className="bg-muted/50">
          <InfoIcon className="h-4 w-4" />
          <AlertDescription className="flex flex-col gap-2">
            <p className="text-sm">
              {t("increaseCapacity.lookingToReceive", "Looking to receive funds instead? You might need incoming capacity.")}
            </p>
            <LinkButton
              to="/channels/inbound"
              variant="outline"
              size="sm"
              className="w-full sm:w-auto"
            >
              {t("increaseCapacity.buyInbound", "Buy Inbound Liquidity")}
            </LinkButton>
          </AlertDescription>
        </Alert>
        <form
          onSubmit={onSubmit}
          className="md:max-w-md max-w-full flex flex-col gap-5 flex-1"
        >
          <div className="grid gap-1.5">
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger type="button">
                  <div className="flex flex-row gap-2 items-center justify-start text-sm">
                    <Label htmlFor="amount">
                      {t("increaseCapacity.increaseSpendingLabel", "Increase spending balance")}
                    </Label>
                    <InfoIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
                  </div>
                </TooltipTrigger>
                <TooltipContent>
                  {t("increaseCapacity.increaseSpendingTooltip", "Configure the amount of spending capacity you need. You will need to deposit on-chain flokicoin to cover the entire channel size, plus on-chain fees.")}
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>

            <CurrencyInput
              id="amount"
              required
              min={scaleInputAmount(100_000, inputUnit)}
              amount={order.amount || ""}
              onAmountChange={(val) => setAmount(val)}
              inputUnit={inputUnit}
              onInputUnitChange={handleInputUnitChange}
            />
            <div className="text-muted-foreground text-sm sensitive slashed-zero">
              {t("increaseCapacity.currentBalance", "Current on-chain balance:")}{" "}
              <FormattedFlokicoinAmount
                amount={balances.onchain.spendable * 1000}
              />
            </div>
            <div className="grid grid-cols-3 gap-1.5 text-muted-foreground text-xs">
              {(inputUnit === "FLC" ? flcPresets : lokiPresets).map((amount) => {
                let displayLabel = amount.toString();
                if (inputUnit === "loki") {
                    if (amount >= 1000000) displayLabel = (amount / 1000000) + "M";
                    else if (amount >= 1000) displayLabel = (amount / 1000) + "k";
                }

                const valueToFill = amount.toString();
                const isActive = order.amount === valueToFill;

                return (
                  <div
                    key={amount}
                    className={cn(
                      "text-center border rounded p-2 cursor-pointer hover:border-muted-foreground",
                      isActive && "border-primary hover:border-primary"
                    )}
                    onClick={() => setAmount(valueToFill)}
                  >
                    {displayLabel}
                  </div>
                );
              })}
            </div>
          </div>
          <>
            {order.paymentMethod === "onchain" && (
              <NewChannelOnchain
                order={order}
                setOrder={setOrder}
                showCustomOptions={true}
              />
            )}

            <div className="mt-2 flex items-top space-x-2">
              <Checkbox
                id="public-channel"
                checked={order.isPublic}
                onCheckedChange={() => setPublic(!order.isPublic)}
                className="me-2"
              />
              <div className="grid gap-1.5 leading-none">
                <Label
                  htmlFor="public-channel"
                  className="flex items-center gap-2"
                >
                  {t("increaseCapacity.publicChannel", "Public Channel")}
                </Label>
                <p className="text-xs text-muted-foreground">
                  {t("increaseCapacity.publicChannelDesc", "Not recommended for most users.")}
                </p>
              </div>
            </div>
          </>
          <MempoolAlert />
          {info?.enableSwap && <SwapAlert swapType="in" />}
          {channels?.some((channel) => channel.public !== !!order.isPublic) && (
            <ChannelPublicPrivateAlert />
          )}
          <DuplicateChannelAlert
            pubkey={order?.pubkey}
            name={"Custom"}
          />
          <Button size="lg">
            <Zap className="me-2 h-4 w-4" />
            {openImmediately ? t("channels.open") : tc("actions.next")}
          </Button>
        </form>

        <div className="flex-1 flex flex-col justify-end items-center gap-4">
          <LinkButton
            to="/channels/inbound"
            variant="link"
            className="text-muted-foreground text-xs"
          >
            {t("increaseCapacity.needIncoming", "Need incoming capacity instead?")}
          </LinkButton>
        </div>
      </div>

      {/* Confirmation Modal */}
      <Dialog open={showConfirmModal} onOpenChange={setShowConfirmModal}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("increaseCapacity.confirmTitle", "Confirm Channel Opening")}</DialogTitle>
            <DialogDescription>
              {t("increaseCapacity.confirmDesc", "Are you sure you want to open a Lightning channel with the following details?")}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-4 text-sm">
              <div>
                <div className="font-medium text-muted-foreground">{t("increaseCapacity.peer", "Peer")}</div>
                <div>{t("increaseCapacity.customPeer", "Custom")}</div>
              </div>
              <div>
                <div className="font-medium text-muted-foreground">{tc("labels.amount", "Amount")}</div>
                <div>
                  <FormattedFlokicoinAmount
                    amount={parseInputAmount(parseFloat(order.amount || "0"), inputUnit) * 1000}
                  />
                </div>
              </div>
              <div>
                <div className="font-medium text-muted-foreground">
                  {t("increaseCapacity.channelType", "Channel Type")}
                </div>
                <div>{order.isPublic ? "Public" : "Private"}</div>
              </div>
              <div>
                <div className="font-medium text-muted-foreground">
                  {t("increaseCapacity.paymentMethod", "Payment Method")}
                </div>
                <div>{t("menu.onchain", "On-chain")}</div>
              </div>
            </div>

            {order.pubkey && (
              <div className="text-sm">
                <div className="font-medium text-muted-foreground">
                  {t("increaseCapacity.nodePublicKey", "Node Public Key")}
                </div>
                <div className="font-mono text-xs break-all bg-muted p-2 rounded">
                  {order.pubkey}
                </div>
              </div>
            )}

            <Alert variant="warning">
              <InfoIcon />
              <AlertDescription>
                <strong>{t("increaseCapacity.important", "Important:")}</strong> {t("increaseCapacity.confirmWarning", "Opening a channel requires an on-chain transaction and network fees. This action cannot be undone. Please verify all details before proceeding.")}
              </AlertDescription>
            </Alert>
          </div>

          <DialogFooter className="gap-2">
            <Button
              variant="outline"
              onClick={() => setShowConfirmModal(false)}
            >
              {tc("actions.cancel", "Cancel")}
            </Button>
            <Button onClick={handleConfirmSubmit}>
              {t("increaseCapacity.confirmAndOpen", "Confirm & Open Channel")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

type NewChannelOnchainProps = {
  order: Partial<OnchainOrder>;
  setOrder: React.Dispatch<React.SetStateAction<Partial<OnchainOrder>>>;
  showCustomOptions: boolean;
};

function NewChannelOnchain(props: NewChannelOnchainProps) {
  const { data: peers } = usePeers();
  const { data: info } = useInfo();
  const { t } = useTranslation("channels");
  const { t: tc } = useTranslation("common");
  
  if (props.order.paymentMethod !== "onchain") {
    throw new Error("unexpected payment method");
  }
  const { pubkey, host } = props.order;
  const { setOrder } = props;
  const isAlreadyPeered =
    pubkey && peers?.some((peer) => peer.nodeId === pubkey);

  const [selection, setSelection] = useState<string>(() => {
      // Initialize state: if order.pubkey is set and matches an LSP, select it.
      // Otherwise if LSPs avail, select first. Else custom.
      if (pubkey && info?.lsps?.find(l => l.pubkey === pubkey)) {
          return pubkey;
      }
      if (info?.lsps && info.lsps.length > 0) {
          return info.lsps[0].pubkey;
      }
      return "custom";
  });

  // Effect to sync selection changes to parent order state
  useEffect(() => {
     if (selection !== "custom") {
         const lsp = info?.lsps?.find(l => l.pubkey === selection);
         if (lsp) {
             setOrder(current => ({
                 ...current,
                 paymentMethod: "onchain",
                 pubkey: lsp.pubkey,
                 host: lsp.host
             }));
         }
     } else if (!pubkey) {
         // If switched to custom and no pubkey set logic? 
         // Actually, do nothing, let user type.
         // But maybe clear if coming from an LSP selection?
         // Let's decided to clear it only if it was an LSP before.
         // For now, simpler: user clears it manually or types over.
     }
  }, [selection, info?.lsps, setOrder]);


  function setPubkey(pubkey: string) {
    props.setOrder((current) => ({
      ...current,
      paymentMethod: "onchain",
      pubkey,
    }));
  }
  const setHost = React.useCallback(
    (host: string) => {
      setOrder((current) => ({
        ...current,
        paymentMethod: "onchain",
        host,
      }));
    },
    [setOrder]
  );

  const { data: nodeDetails } = useNodeDetails(pubkey);

  React.useEffect(() => {
    // Only auto-fill host if custom selection, or if we want to ensure host is set?
    // If LSP is selected, we parsed URI above.
    const socketAddress = nodeDetails?.sockets?.split(",")?.[0];
    if (socketAddress && selection === "custom") {
      setHost(socketAddress);
    }
  }, [nodeDetails, setHost, selection]);

  const hasLSPs = info?.lsps && info.lsps.length > 0;

  return (
    <>
      <div className="flex flex-col gap-5">
        {props.showCustomOptions && (
          <>
            <div className="grid gap-1.5">
                <Label htmlFor="provider-select">{t("increaseCapacity.peerLabel")}</Label>
                {/* LSP Selector */}
                <Select 
                    value={selection} 
                    onValueChange={(val) => {
                        setSelection(val);
                        if (val === "custom") {
                            setPubkey("");
                            setHost("");
                        }
                    }}
                    disabled={!hasLSPs}
                >
                    <SelectTrigger className="w-full" id="provider-select">
                        <SelectValue placeholder={!hasLSPs ? t("increaseCapacity.noLsps", "No LSPs Configured") : t("increaseCapacity.selectProvider", "Select a Provider")} />
                    </SelectTrigger>
                    <SelectContent>
                        {info?.lsps?.map((lsp) => (
                            <SelectItem key={lsp.pubkey} value={lsp.pubkey}>
                                {lsp.name || lsp.pubkey.substring(0, 16) + "..."}
                            </SelectItem>
                        ))}
                        {hasLSPs && <div className="h-px bg-muted my-1" />}
                        <SelectItem value="custom">{t("increaseCapacity.customPeer")}</SelectItem>
                    </SelectContent>
                </Select>
                {!hasLSPs && (
                     <div className="text-muted-foreground text-xs">
                        <span className="me-1">{t("increaseCapacity.manageProviders", "Manage providers in")}</span>
                        <LinkButton to="/settings/services" variant="link" className="h-auto p-0 text-xs underline">
                            {tc("nav.settings")} &gt; Services
                        </LinkButton>
                     </div>
                )}
            </div>

            {/* Manual Input Fields - Show if selection is Custom */}
            {selection === "custom" && (
                <>
                    <div className="grid gap-1.5">
                    <Label htmlFor="pubkey">{t("increaseCapacity.peer")}</Label>
                    <Input
                        id="pubkey"
                        type="text"
                        dir="ltr"
                        value={pubkey}
                        required
                        placeholder="Pubkey of the peer"
                        onChange={(e) => {
                        const parts = e.target.value.trim().split("@");
                        setPubkey(parts[0]);
                        if (parts.length > 1) {
                            setHost(parts[1]);
                        }
                        }}
                    />
                    {nodeDetails && (
                        <div className="ms-2 text-muted-foreground text-sm">
                        <span
                            className="me-2"
                            style={{ color: `${nodeDetails.color}` }}
                        >
                            ⬤
                        </span>
                        {nodeDetails.alias && (
                            <>
                            {nodeDetails.alias} ({nodeDetails.active_channel_count}{" "}
                            channels)
                            </>
                        )}
                        </div>
                    )}
                    </div>

                    {!isAlreadyPeered && /*!nodeDetails && */ pubkey && (
                    <div className="grid gap-1.5">
                        <Label htmlFor="host">{t("increaseCapacity.hostPort")}</Label>
                        <Input
                        id="host"
                        type="text"
                        dir="ltr"
                        value={host}
                        required
                        placeholder="0.0.0.0:5521 or [2600::]:5521"
                        onChange={(e) => {
                            setHost(e.target.value.trim());
                        }}
                        />
                    </div>
                    )}
                </>
            )}
          </>
        )}
      </div>
    </>
  );
}
