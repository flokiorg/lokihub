import { ClipboardPasteIcon, InfoIcon, SettingsIcon } from "lucide-react";
import React from "react";
import { toast } from "sonner";
import { ResetRoutingDataDialogContent } from "src/components/ResetRoutingDataDialogContent";
import SettingsHeader from "src/components/SettingsHeader";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "src/components/ui/alert-dialog";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { RadioGroup, RadioGroupItem } from "src/components/ui/radio-group";
import { Textarea } from "src/components/ui/textarea";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "src/components/ui/tooltip";
import { localStorageKeys } from "src/constants";
import { useInfo } from "src/hooks/useInfo";
import { useUnit } from "src/hooks/useUnit";

import { request } from "src/utils/request";

type Props = {
  apiRequest: (
    endpoint: string,
    method: string,
    requestBody?: object
  ) => Promise<void>;
  target?: string;
};

function ProbeInvoiceDialogContent({ apiRequest }: Props) {
  const [invoice, setInvoice] = React.useState<string>();

  async function onConfirm() {
    await apiRequest("/api/send-payment-probes", "POST", {
      invoice: invoice,
    });
    setInvoice("");
  }

  return (
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>Probe Invoice</AlertDialogTitle>
        <AlertDialogDescription className="text-start">
          <Label htmlFor="invoice" className="block mb-2">
            Enter Invoice
          </Label>
          <Input
            id="invoice"
            name="invoice"
            type="text"
            placeholder="lnbc...."
            required
            autoFocus
            value={invoice}
            onChange={(e) => {
              setInvoice(e.target.value.trim());
            }}
          />
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>Cancel</AlertDialogCancel>
        <AlertDialogAction disabled={!invoice} onClick={onConfirm}>
          Confirm
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  );
}
function ProbeKeysendDialogContent({ apiRequest }: Props) {
  const [amount, setAmount] = React.useState<string>("");
  const [nodeId, setNodeId] = React.useState<string>("");
  const { parseAmount } = useUnit();

  async function onConfirm() {
    await apiRequest("/api/send-spontaneous-payment-probes", "POST", {
      amount: parseAmount(parseFloat(amount) || 0) * 1000,
      nodeId,
    });

    setAmount("");
    setNodeId("");
  }

  return (
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>Probe Keysend</AlertDialogTitle>
        <AlertDialogDescription className="text-start">
          <div>
            <Label htmlFor="amount" className="block mb-2">
              Enter Amount
            </Label>
            <Input
              id="amount"
              name="amount"
              type="number"
              min={0}
              step="any"
              autoFocus
              value={amount}
              onChange={(e) => {
                setAmount(e.target.value.trim());
              }}
            />
          </div>
          <div className="mt-4">
            <Label htmlFor="pubkey" className="block mb-2">
              Enter Node Pubkey
            </Label>
            <Input
              id="pubkey"
              type="text"
              value={nodeId}
              onChange={(e) => {
                setNodeId(e.target.value.trim());
              }}
            />
          </div>
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>Cancel</AlertDialogCancel>
        <AlertDialogAction
          disabled={!parseFloat(amount) || !nodeId}
          onClick={onConfirm}
        >
          Confirm
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  );
}

function RefundSwapDialogContent() {
  const [swapId, setSwapId] = React.useState<string>("");
  const [address, setAddress] = React.useState<string>("");
  const [isInternal, setInternal] = React.useState<boolean>(true);

  async function onConfirm() {
    try {
      const response = await request("/api/swaps/refund", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          swapId,
          ...(address ? { address } : {}),
        }),
      });
      console.info("Processed refund", response);
      toast("Refund transaction broadcasted");
    } catch (error) {
      console.error(error);
      toast.error("Failed to process refund", {
        description: "" + error,
      });
    }
    setSwapId("");
  }

  const paste = async () => {
    const text = await navigator.clipboard.readText();
    setAddress(text.trim());
  };

  return (
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle className="capitalize">Refund Swap</AlertDialogTitle>
        <AlertDialogDescription className="flex text-foreground flex-col gap-4">
          <div className="flex flex-row gap-1 items-center text-muted-foreground">
            Only On-chain {"->"} Lightning swaps need to be refunded
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="swapId">
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger>
                    <div className="flex flex-row gap-1 items-center text-muted-foreground">
                      Swap Id
                      <InfoIcon className="h-4 w-4 shrink-0" />
                    </div>
                  </TooltipTrigger>
                  <TooltipContent>
                    <p>
                      To find the Swap ID, close this dialog and click on the
                      "List Swaps" button. Then you can look through and find a
                      swap that is in state "FAILED" and matches the amount you
                      tried to swap. The latest swaps are at the bottom of the
                      list.
                    </p>
                    <p className="mt-2">
                      When you have found the swap, copy the value of the id
                      field. The swap Id will look something like uNHoD8QrAr9b.
                    </p>
                  </TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </Label>
            <Input
              id="swapId"
              name="swapId"
              type="text"
              required
              autoFocus
              value={swapId}
              onChange={(e) => {
                setSwapId(e.target.value.trim());
              }}
            />
          </div>
          <div className="flex flex-col gap-4">
            <Label>Refund to</Label>
            <RadioGroup
              defaultValue="normal"
              value={isInternal ? "internal" : "external"}
              onValueChange={() => {
                setAddress("");
                setInternal(!isInternal);
              }}
              className="flex gap-4 flex-row"
            >
              <div className="flex items-start space-x-2 mb-2">
                <RadioGroupItem
                  value="internal"
                  id="internal"
                  className="shrink-0"
                />
                <Label
                  htmlFor="internal"
                  className="font-medium cursor-pointer"
                >
                  On-chain balance
                </Label>
              </div>
              <div className="flex items-start space-x-2">
                <RadioGroupItem
                  value="external"
                  id="external"
                  className="shrink-0"
                />
                <Label
                  htmlFor="external"
                  className="font-medium cursor-pointer"
                >
                  External on-chain wallet
                </Label>
              </div>
            </RadioGroup>
          </div>
          {!isInternal && (
            <div className="grid gap-1.5">
              <Label>On-chain address</Label>
              <div className="flex gap-2">
                <Input
                  placeholder="bc1..."
                  value={address}
                  onChange={(e) => setAddress(e.target.value)}
                  required
                />
                <Button
                  type="button"
                  variant="outline"
                  className="px-2"
                  onClick={paste}
                >
                  <ClipboardPasteIcon className="w-4 h-4" />
                </Button>
              </div>
            </div>
          )}
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>Cancel</AlertDialogCancel>
        <AlertDialogAction
          disabled={!swapId || (!isInternal && !address)}
          onClick={onConfirm}
        >
          Confirm
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  );
}

function GetNetworkGraphDialogContent({ apiRequest }: Props) {
  const [nodeIds, setNodeIds] = React.useState<string>("");

  async function onConfirm() {
    await apiRequest(`/api/node/network-graph?nodeIds=${nodeIds}`, "GET");
    setNodeIds("");
  }

  return (
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>Get Network Graph</AlertDialogTitle>
        <AlertDialogDescription className="text-start">
          <Label htmlFor="nodes" className="block mb-2">
            Enter Node Pubkeys (separated by commas)
          </Label>
          <Input
            id="nodes"
            type="text"
            placeholder="e.g. nodepubkey1,nodepubkey2,nodepubkey3"
            value={nodeIds}
            onChange={(e) => {
              setNodeIds(e.target.value.trim());
            }}
          />
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>Cancel</AlertDialogCancel>
        <AlertDialogAction disabled={!nodeIds} onClick={onConfirm}>
          Confirm
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  );
}

// LogSettingsDialogContent is its own, self-contained AlertDialog (not part
// of the shared one the button grid uses) — there's exactly one possible
// piece of content for it, so it doesn't need the grid's dialog-union state
// to pick what to render.
function LogSettingsDialogContent({
  logMaxLen,
  onSave,
}: {
  logMaxLen: string;
  onSave: (value: string) => void;
}) {
  const [value, setValue] = React.useState(logMaxLen);

  // Re-seed from the current saved value each time the dialog opens, so a
  // cancelled edit doesn't leave stale typing behind for next time.
  React.useEffect(() => {
    setValue(logMaxLen);
  }, [logMaxLen]);

  return (
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>Log Settings</AlertDialogTitle>
        <AlertDialogDescription className="text-start">
          <Label htmlFor="logMaxLength" className="block mb-2">
            Max Length (characters)
          </Label>
          <Input
            id="logMaxLength"
            type="number"
            min={1}
            autoFocus
            value={value}
            onChange={(e) => setValue(e.target.value.trim())}
          />
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>Cancel</AlertDialogCancel>
        <AlertDialogAction
          disabled={!parseInt(value)}
          onClick={() => onSave(value)}
        >
          Save
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  );
}

export default function DebugTools() {
  const [apiResponse, setApiResponse] = React.useState<string>("");
  const [dialog, setDialog] = React.useState<
    | "probeInvoice"
    | "probeKeysend"
    | "refundSwap"
    | "getNetworkGraph"
    | "resetRoutingData"
  >();
  // Get App/Node Logs fetch immediately with whatever this is currently set
  // to — it's configured separately (the Logs card's settings icon), not
  // typed into a blocking dialog before every fetch — and persisted, so a
  // value set to chase down something long doesn't reset on reload.
  const [logMaxLen, setLogMaxLenState] = React.useState<string>(
    () => localStorage.getItem(localStorageKeys.debugLogMaxLen) || "5000"
  );
  function setLogMaxLen(value: string) {
    setLogMaxLenState(value);
    localStorage.setItem(localStorageKeys.debugLogMaxLen, value);
  }

  const { hasChannelManagement } = useInfo();

  async function apiRequest(
    endpoint: string,
    method: string,
    requestBody?: object
  ) {
    try {
      const requestOptions: RequestInit = {
        method: method,
        headers: {
          "Content-Type": "application/json",
        },
      };

      if (requestBody) {
        requestOptions.body = JSON.stringify(requestBody);
      }

      const data = await request(endpoint, requestOptions);

      setApiResponse(
        (data as { logs: string }).logs || JSON.stringify(data, null, 2)
      );
    } catch (error) {
      setApiResponse(JSON.stringify(error, Object.getOwnPropertyNames(error)));
    }
  }

  return (
    <div>
      <SettingsHeader
        title="Debug Tools"
        description="Extra tools for debugging purposes."
      />
      <div className="grid mt-6 gap-6 mb-8 lg:mb-8 md:grid-cols-2 xl:grid-cols-3">
        <AlertDialog
          onOpenChange={() => {
            if (!open) {
              setDialog(undefined);
            }
          }}
        >
          <Button
            variant="outline"
            onClick={() => apiRequest("/api/info", "GET")}
          >
            Get Info
          </Button>
          <Button
            variant="outline"
            onClick={() => apiRequest("/api/peers", "GET")}
          >
            List Peers
          </Button>
          <Button
            variant="outline"
            onClick={() => apiRequest("/api/channels", "GET")}
          >
            List Channels
          </Button>
          {hasChannelManagement && (
            <>
              <Button
                variant={"outline"}
                onClick={() => apiRequest("/api/swaps", "GET")}
              >
                List Swaps
              </Button>
              <AlertDialogTrigger asChild>
                <Button
                  variant={"outline"}
                  onClick={() => setDialog("refundSwap")}
                >
                  Refund Swap
                </Button>
              </AlertDialogTrigger>
              <Button
                variant={"outline"}
                onClick={() => apiRequest("/api/swaps/mnemonic", "GET")}
              >
                Get Swap Mnemonic
              </Button>
            </>
          )}
          <Button
            variant="outline"
            onClick={() =>
              apiRequest(`/api/log/app?maxLen=${logMaxLen}`, "GET")
            }
          >
            Get App Logs
          </Button>
          <Button
            variant="outline"
            onClick={() =>
              apiRequest(`/api/log/node?maxLen=${logMaxLen}`, "GET")
            }
          >
            Get Node Logs
          </Button>
          <Button
            variant="outline"
            onClick={() => {
              apiRequest(`/api/node/status`, "GET");
            }}
          >
            Get Node Status
          </Button>
          <Button
            variant="outline"
            onClick={() => {
              apiRequest(`/api/balances`, "GET");
            }}
          >
            Get Balances
          </Button>
          <AlertDialogTrigger asChild>
            <Button
              variant="outline"
              onClick={() => setDialog("getNetworkGraph")}
            >
              Get Network Graph
            </Button>
          </AlertDialogTrigger>
          <Button
            variant="outline"
            onClick={() => {
              apiRequest(`/api/commands`, "GET");
            }}
          >
            Get Node Commands
          </Button>
          {/* probing functions are not useful */}
          {/*info?.backendType === "LDK" && (
            <AlertDialogTrigger asChild>
              <Button onClick={() => setDialog("probeInvoice")}>
                Probe Invoice
              </Button>
            </AlertDialogTrigger>
          )*/}
          {/*info?.backendType === "LDK" && (
            <AlertDialogTrigger asChild>
              <Button onClick={() => setDialog("probeKeysend")}>
                Probe Keysend
              </Button>
            </AlertDialogTrigger>
          )*/}

          {dialog === "probeInvoice" && (
            <ProbeInvoiceDialogContent apiRequest={apiRequest} />
          )}
          {dialog === "probeKeysend" && (
            <ProbeKeysendDialogContent apiRequest={apiRequest} />
          )}
          {dialog === "refundSwap" && <RefundSwapDialogContent />}
          {dialog === "getNetworkGraph" && (
            <GetNetworkGraphDialogContent apiRequest={apiRequest} />
          )}
          {dialog === "resetRoutingData" && <ResetRoutingDataDialogContent />}
        </AlertDialog>
      </div>
      {apiResponse && (
        <Card>
          <CardHeader className="pb-3 flex flex-row items-start justify-between gap-2">
            <CardTitle className="text-base">Response</CardTitle>
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="ghost" size="icon" aria-label="Log settings">
                  <SettingsIcon className="size-4" />
                </Button>
              </AlertDialogTrigger>
              <LogSettingsDialogContent
                logMaxLen={logMaxLen}
                onSave={setLogMaxLen}
              />
            </AlertDialog>
          </CardHeader>
          <CardContent>
            <Textarea
              className="whitespace-pre-wrap break-anywhere font-mono"
              rows={35}
              value={`API Response: ${apiResponse}`}
            />
          </CardContent>
        </Card>
      )}
    </div>
  );
}
