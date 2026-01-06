import React from "react";
import {
    ConnectPeerRequest,
    MempoolUtxo,
    NewChannelOrder,
    OpenChannelRequest,
    OpenChannelResponse,
    PayInvoiceResponse,
} from "src/types";

import { CopyIcon, QrCodeIcon, RefreshCwIcon } from "lucide-react";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import QRCode from "src/components/QRCode";
import { Button } from "src/components/ui/button";
import {
    Card,
    CardContent,
    CardDescription,
    CardFooter,
    CardHeader,
    CardTitle,
} from "src/components/ui/card";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
    DialogTrigger,
} from "src/components/ui/dialog";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { Separator } from "src/components/ui/separator";
import { Table, TableBody, TableCell, TableRow } from "src/components/ui/table";
import {
    Tooltip,
    TooltipContent,
    TooltipProvider,
    TooltipTrigger,
} from "src/components/ui/tooltip";
import { useBalances } from "src/hooks/useBalances";

import { ChannelWaitingForConfirmations } from "src/components/channels/ChannelWaitingForConfirmations";
import { PayLightningInvoice } from "src/components/PayLightningInvoice";
import TwoColumnLayoutHeader from "src/components/TwoColumnLayoutHeader";
import { LinkButton } from "src/components/ui/custom/link-button";
import { useChannels } from "src/hooks/useChannels";
import { useMempoolApi } from "src/hooks/useMempoolApi";
import { useNodeDetails } from "src/hooks/useNodeDetails";
import { useOnchainAddress } from "src/hooks/useOnchainAddress";
import { usePeers } from "src/hooks/usePeers";
import { useSyncWallet } from "src/hooks/useSyncWallet";
import { copyToClipboard } from "src/lib/clipboard";
import { splitSocketAddress } from "src/lib/utils";
import useChannelOrderStore from "src/state/ChannelOrderStore";
import { LSPOrderRequest, LSPOrderResponse } from "src/types";
import { request } from "src/utils/request";

// ensures React does not open a duplicate channel
// this is a hack and will break if the user tries to open
// 2 outbound channels without refreshing the page (I think an edge case)


export function CurrentChannelOrder() {
  const order = useChannelOrderStore((store) => store.order);
  if (!order) {
    return (
      <p>
        No pending channel order.{" "}
        <Link to="/channels" className="underline">
          Return to channels page
        </Link>
      </p>
    );
  }
  return <ChannelOrderInternal order={order} />;
}

function ChannelOrderInternal({ order }: { order: NewChannelOrder }) {
  useSyncWallet();
  switch (order.status) {
    case "pay":
      switch (order.paymentMethod) {
        case "onchain":
          return <PayFlokicoinChannelOrder order={order} />;
        case "lightning":
          return <PayLightningChannelOrder order={order} />;
        default:
          break;
      }
      break;
    case "paid":
      // LSPS1 only
      return <PaidLightningChannelOrder />;
    case "opening":
      return <ChannelOpening fundingTxId={order.fundingTxId} />;
    case "success":
      return <Success />;
    default:
      break;
  }

  return (
    <p>
      TODO: {order.status} {order.paymentMethod}
    </p>
  );
}

function Success() {
  return (
    <div className="flex flex-col justify-center gap-5 p-5 max-w-md items-stretch">
      <TwoColumnLayoutHeader
        title="Channel Opened"
        description="Your new lightning channel is ready to use"
      />

      <p>
        Congratulations! Your channel is active and can be used to send and
        receive payments.
      </p>
      <p>
        To ensure you can both send and receive, make sure to balance your channel's liquidity.
      </p>

      <LinkButton to="/home" className="flex justify-center mt-8">
        Go to your dashboard
      </LinkButton>
    </div>
  );
}

function ChannelOpening({ fundingTxId }: { fundingTxId: string | undefined }) {
  const { data: channels } = useChannels(true);
  const channel = fundingTxId
    ? channels?.find((channel) => channel.fundingTxId === fundingTxId)
    : undefined;

  React.useEffect(() => {
    if (channel?.active) {
      useChannelOrderStore.getState().updateOrder({
        status: "success",
      });
    }
  }, [channel]);

  if (!channel) {
    return <Loading />;
  }

  return <ChannelWaitingForConfirmations channel={channel} />;
}

function useEstimatedTransactionFee() {
  const { data: recommendedFees } = useMempoolApi<{ fastestFee: number }>(
    "/v1/fees/recommended",
    true
  );
  if (recommendedFees?.fastestFee) {
    // estimated transaction size: 200 vbytes
    return 200 * recommendedFees.fastestFee;
  }
}

// TODO: move these to new files
function PayFlokicoinChannelOrder({ order }: { order: NewChannelOrder }) {
  if (order.paymentMethod !== "onchain") {
    throw new Error("incorrect payment method");
  }
  const { data: balances } = useBalances(true);

  if (!balances) {
    return <Loading />;
  }

  // expect at least the user to have more funds than the channel size, hopefully enough to cover mempool fees.
  if (balances.onchain.spendable > +order.amount) {
    return <PayFlokicoinChannelOrderWithSpendableFunds order={order} />;
  }
  if (balances.onchain.total > +order.amount) {
    return <PayFlokicoinChannelOrderWaitingDepositConfirmation />;
  }
  return <PayFlokicoinChannelOrderTopup order={order} />;
}

function PayFlokicoinChannelOrderWaitingDepositConfirmation() {
  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle className="flex flex-row items-center gap-2">
            Flokicoin deposited
          </CardTitle>
        </CardHeader>
        <CardContent className="flex items-center gap-2">
          <Loading /> Waiting for one block confirmation
        </CardContent>
        <CardFooter className="text-muted-foreground">
          estimated time: 10 minutes
        </CardFooter>
      </Card>
    </>
  );
}

function PayFlokicoinChannelOrderTopup({ order }: { order: NewChannelOrder }) {
  if (order.paymentMethod !== "onchain") {
    throw new Error("incorrect payment method");
  }

  const { data: channels } = useChannels();

  const { data: balances } = useBalances();
  const {
    data: onchainAddress,
    getNewAddress,
    loadingAddress,
  } = useOnchainAddress();

  const { data: mempoolAddressUtxos } = useMempoolApi<MempoolUtxo[]>(
    onchainAddress ? `/address/${onchainAddress}/utxo` : undefined,
    3000
  );
  const estimatedTransactionFee = useEstimatedTransactionFee();

  if (!onchainAddress || !balances || !estimatedTransactionFee) {
    return (
      <div className="flex justify-center">
        <Loading />
      </div>
    );
  }

  // expect at least the user to have more funds than the channel size, hopefully enough to cover mempool fees.
  // This only considers one UTXO and will not work well if the user generates a new address.
  // However, this is just a fallback because LDK only updates onchain balances ~ once per minute.
  const unspentAmount =
    mempoolAddressUtxos?.map((utxo) => utxo.value).reduce((a, b) => a + b, 0) ||
    0;

  if (unspentAmount > +order.amount) {
    return <PayFlokicoinChannelOrderWaitingDepositConfirmation />;
  }

  const num0ConfChannels =
    channels?.filter((c) => c.confirmationsRequired === 0).length || 0;

  const estimatedAnchorReserve = Math.max(
    num0ConfChannels * 25000 - balances.onchain.reserved,
    0
  );

  const missingAmount =
    +order.amount +
    estimatedTransactionFee +
    estimatedAnchorReserve -
    balances.onchain.total;

  const recommendedAmount = Math.ceil(missingAmount / 10000) * 10000;

  return (
    <div className="grid gap-5">
      <AppHeader
        title="Deposit flokicoin"
        description="You don't have enough Flokicoin to open your intended channel"
      />
      <div className="grid gap-5 max-w-lg">
        <div className="grid gap-1.5">
          <Label htmlFor="text">On-Chain Address</Label>
          <p className="text-xs slashed-zero">
            You currently have{" "}
            <span className="font-semibold sensitive">
              <FormattedFlokicoinAmount amount={balances.onchain.total * 1000} />
            </span>
            . We recommend depositing an additional amount of{" "}
            <span className="font-semibold">
              <FormattedFlokicoinAmount amount={recommendedAmount * 1000} />
            </span>{" "}
            to open this channel.
          </p>
          <p className="text-xs text-muted-foreground">
            This amount includes cost for the channel opening and potential
            channel onchain reserves.
          </p>
          <div className="flex flex-row gap-2 items-center">
            <Input
              type="text"
              value={onchainAddress}
              readOnly
              className="flex-1"
            />
            <Button
              variant="secondary"
              size="icon"
              onClick={() => {
                copyToClipboard(onchainAddress);
              }}
            >
              <CopyIcon className="size-4" />
            </Button>
            <Dialog>
              <DialogTrigger asChild>
                <Button variant="secondary" size="icon">
                  <QrCodeIcon className="size-4" />
                </Button>
              </DialogTrigger>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>Deposit flokicoin</DialogTitle>
                  <DialogDescription>
                    Scan this QR code with your wallet to send funds.
                  </DialogDescription>
                </DialogHeader>
                <div className="flex flex-row justify-center p-3">
                  <a href={`flokicoin:${onchainAddress}`} target="_blank">
                    <QRCode value={onchainAddress} />
                  </a>
                </div>
              </DialogContent>
            </Dialog>
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger asChild>
                  <LoadingButton
                    variant="secondary"
                    size="icon"
                    onClick={getNewAddress}
                    loading={loadingAddress}
                    className="w-9 h-9"
                  >
                    {!loadingAddress && <RefreshCwIcon className="size-4" />}
                  </LoadingButton>
                </TooltipTrigger>
                <TooltipContent>Generate a new address</TooltipContent>
              </Tooltip>
            </TooltipProvider>
          </div>
        </div>

        <Card>
          <CardHeader>
            <CardTitle className="flex flex-row items-center gap-2">
              <Loading /> Waiting for your transaction
            </CardTitle>
            <CardDescription>
              Send a flokicoin transaction to the address provided above. You'll
              be redirected as soon as the transaction is seen in the mempool.
            </CardDescription>
          </CardHeader>
          {unspentAmount > 0 && (
            <CardContent className="slashed-zero">
              <FormattedFlokicoinAmount amount={unspentAmount * 1000} /> deposited
            </CardContent>
          )}
        </Card>


      </div>
    </div>
  );
}

function PayFlokicoinChannelOrderWithSpendableFunds({
  order,
}: {
  order: NewChannelOrder;
}) {
  if (order.paymentMethod !== "onchain") {
    throw new Error("incorrect payment method");
  }
  const { data: peers } = usePeers();

  const { pubkey, host } = order;

  const { data: nodeDetails } = useNodeDetails(pubkey);

  const connectPeer = React.useCallback(async () => {
    if (!nodeDetails && !host) {
      throw new Error("node details not found");
    }
    const socketAddress = nodeDetails?.sockets
      ? nodeDetails.sockets.split(",")[0]
      : host;

    const { address, port } = splitSocketAddress(socketAddress);

    if (!address || !port) {
      throw new Error("host not found");
    }
    console.info(`🔌 Peering with ${pubkey}`);
    const connectPeerRequest: ConnectPeerRequest = {
      pubkey,
      address,
      port: +port,
    };
    await request("/api/peers", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(connectPeerRequest),
    });
  }, [nodeDetails, pubkey, host]);

  const [error, setError] = React.useState<string | undefined>();

  const openChannel = React.useCallback(async () => {
    setError(undefined);
    try {
      if (order.paymentMethod !== "onchain") {
        throw new Error("incorrect payment method");
      }

      if (!peers) {
        throw new Error("peers not loaded");
      }

      // only pair if necessary
      // also allows to open channel to existing peer without providing a socket address.
      if (!peers.some((peer) => peer.nodeId === pubkey)) {
        await connectPeer();
      }

      console.info(`🎬 Opening channel with ${pubkey}`);

      const openChannelRequest: OpenChannelRequest = {
        pubkey,
        amountLoki: +order.amount,
        public: order.isPublic,
      };
      const openChannelResponse = await request<OpenChannelResponse>(
        "/api/channels",
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
          },
          body: JSON.stringify(openChannelRequest),
        }
      );

      if (!openChannelResponse?.fundingTxId) {
        throw new Error("No funding txid in response");
      }
      console.info(
        "Channel opening transaction published",
        openChannelResponse.fundingTxId
      );
      toast("Successfully published channel opening transaction");
      useChannelOrderStore.getState().updateOrder({
        fundingTxId: openChannelResponse.fundingTxId,
        status: "opening",
      });
    } catch (error) {
      console.error(error);
      const errorMessage = "" + error;
      setError(errorMessage);
      toast.error("Something went wrong", {
        description: errorMessage,
      });
    }
  }, [
    connectPeer,
    order.amount,
    order.isPublic,
    order.paymentMethod,
    peers,
    pubkey,
  ]);

  const hasStartedOpenedChannel = React.useRef(false);

  React.useEffect(() => {
    if (!peers || hasStartedOpenedChannel.current) {
      return;
    }

    hasStartedOpenedChannel.current = true;
    openChannel();
  }, [openChannel, order.amount, peers, pubkey]);

  if (error) {
    return (
      <div className="flex flex-col gap-5">
        <AppHeader
          title="Error Opening Channel"
          description="Something went wrong while opening the channel"
        />
        <div className="flex flex-col gap-5">
          <p className="text-destructive break-all">{error}</p>
          <div className="flex gap-2">
            <Button onClick={openChannel}>Retry</Button>
            <LinkButton to="/channels/outgoing" variant="secondary">
              Cancel
            </LinkButton>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-5">
      <AppHeader
        title="Opening channel"
        description="Your funds have been successfully deposited"
      />

      <div className="flex flex-col gap-5">
        <Loading />
        <p>Please wait...</p>
      </div>
    </div>
  );
}

function useWaitForNewChannel() {
  const order = useChannelOrderStore((store) => store.order);
  const { data: channels } = useChannels(true);

  const newChannel =
    channels && order?.prevChannelIds
      ? channels.find(
          (newChannel) =>
            !order.prevChannelIds.some(
              (current) => newChannel.id === current
            ) && newChannel.fundingTxId
        )
      : undefined;

  React.useEffect(() => {
    if (newChannel) {
      useChannelOrderStore.getState().updateOrder({
        status: "opening",
        fundingTxId: newChannel.fundingTxId,
      });
    }
  }, [newChannel]);
}

function PaidLightningChannelOrder() {
  useWaitForNewChannel();

  return (
    <div className="flex w-full h-full gap-2 items-center justify-center">
      <Loading /> <p>Waiting for channel to be opened...</p>
    </div>
  );
}

function PayLightningChannelOrder({ order }: { order: NewChannelOrder }) {
  if (order.paymentMethod !== "lightning") {
    throw new Error("incorrect payment method");
  }
  const { data: balances } = useBalances();
  const { data: channels } = useChannels(true);
  const [, setRequestedInvoice] = React.useState(false);

  const [lspOrderResponse, setLspOrderResponse] = React.useState<
    LSPOrderResponse | undefined
  >();

  useWaitForNewChannel();

  React.useEffect(() => {
    if (!channels) {
      return;
    }
    setRequestedInvoice((current) => {
      if (!current) {
        (async () => {
          try {
            if (!order.lspType || !order.lspIdentifier) {
              throw new Error("missing lsp info in order");
            }
            const newLSPOrderRequest: LSPOrderRequest = {
              lspType: order.lspType,
              lspIdentifier: order.lspIdentifier,
              amount: parseInt(order.amount),
              public: order.isPublic,
            };
            const response = await request<LSPOrderResponse>(
              "/api/lsp-orders",
              {
                method: "POST",
                headers: {
                  "Content-Type": "application/json",
                },
                body: JSON.stringify(newLSPOrderRequest),
              }
            );
            if (!response) {
              throw new Error("no LSP order response");
            }

            if (!response.invoice) {
              // assume payment is handled by Loki Account
              // we will wait for a channel to be opened to us
              useChannelOrderStore.getState().updateOrder({
                status: "paid",
              });
            }
            setLspOrderResponse(response);
          } catch (error) {
            toast.error("Something went wrong", {
              description: "" + error,
            });
          }
        })();
      }
      return true;
    });
  }, [
    channels,
    order.amount,
    order.isPublic,
    order.lspType,
    order.lspIdentifier,
  ]);

  const canPayInternally =
    balances &&
    lspOrderResponse &&
    balances.lightning.nextMaxSpendableMPP / 1000 >
      lspOrderResponse.invoiceAmount;
  const [isPaying, setPaying] = React.useState(false);
  const [payExternally, setPayExternally] = React.useState(false);

  return (
    <div className="flex flex-col gap-5">
      <AppHeader
        title="Review Channel Purchase"
        description={
          lspOrderResponse
            ? "Complete Payment to open a channel to your node"
            : "Please wait, loading..."
        }
      />
      {!lspOrderResponse?.invoice && <Loading />}

      {lspOrderResponse?.invoice && (
        <>
          <div className="max-w-md flex flex-col gap-5">
            <div className="border rounded-lg slashed-zero">
              <Table>
                <TableBody>
                  {lspOrderResponse.outgoingLiquidity > 0 && (
                    <TableRow>
                      <TableCell className="font-medium p-3">
                        Spending Balance
                      </TableCell>
                      <TableCell className="text-right p-3">
                        <FormattedFlokicoinAmount
                          amount={lspOrderResponse.outgoingLiquidity * 1000}
                        />
                      </TableCell>
                    </TableRow>
                  )}
                  {lspOrderResponse.incomingLiquidity > 0 && (
                    <TableRow>
                      <TableCell className="font-medium p-3">
                        Incoming Liquidity
                      </TableCell>
                      <TableCell className="text-right p-3">
                        <div className="flex flex-col items-end">
                          <FormattedFlokicoinAmount
                            amount={lspOrderResponse.incomingLiquidity * 1000}
                          />
                          <FormattedFiatAmount
                            amount={lspOrderResponse.incomingLiquidity}
                            showApprox
                          />
                        </div>
                      </TableCell>
                    </TableRow>
                  )}
                  <TableRow>
                    <TableCell className="font-medium p-3">
                      Amount to pay
                    </TableCell>
                    <TableCell className="font-semibold text-right p-3">
                      <div className="flex flex-col items-end">
                        <FormattedFlokicoinAmount
                          amount={lspOrderResponse.invoiceAmount * 1000}
                        />
                        <FormattedFiatAmount
                          amount={lspOrderResponse.invoiceAmount}
                          showApprox
                        />
                      </div>
                    </TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </div>
            <div className="flex justify-center w-full -mb-5">
              <p className="text-center text-xs text-muted-foreground max-w-sm">
                By proceeding, you consent the channel opens immediately and
                that you lose the right to revoke once it is open.
              </p>
            </div>
            <>
              {canPayInternally && (
                <>
                  <LoadingButton
                    loading={isPaying}
                    className="mt-4"
                    onClick={async () => {
                      try {
                        setPaying(true);

                        // NOTE: for amboss this will not return until the HOLD invoice is settled
                        // which is after the channel has N block confirmations
                        await request<PayInvoiceResponse>(
                          `/api/payments/${lspOrderResponse.invoice}`,
                          {
                            method: "POST",
                            headers: {
                              "Content-Type": "application/json",
                            },
                          }
                        );

                        useChannelOrderStore.getState().updateOrder({
                          status: "paid",
                        });
                        toast("Channel successfully requested");
                      } catch (e) {
                        toast.error("Failed to send: ", {
                          description: "" + e,
                        });
                        console.error(e);
                      }
                      setPaying(false);
                    }}
                  >
                    Pay and open channel
                  </LoadingButton>
                  {!payExternally && (
                    <Button
                      type="button"
                      variant="link"
                      className="text-muted-foreground text-xs"
                      onClick={() => setPayExternally(true)}
                    >
                      Pay with another wallet
                    </Button>
                  )}
                </>
              )}

              {(payExternally || !canPayInternally) && (
                <div className="flex flex-row justify-center">
                  <PayLightningInvoice invoice={lspOrderResponse.invoice} />
                </div>
              )}

              <div className="flex-1 flex flex-col justify-end items-center gap-4">
                <Separator className="my-16" />
                <p className="text-sm text-muted-foreground text-center">
                  Other options
                </p>
                <LinkButton
                  to="/channels/outgoing"
                  variant="secondary"
                  className="w-full"
                >
                  Increase Spending Balance
                </LinkButton>
              </div>
            </>
          </div>
        </>
      )}
    </div>
  );
}
