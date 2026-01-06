import {
    Card,
    CardContent,
    CardFooter,
    CardHeader,
    CardTitle,
} from "src/components/ui/card";

import { NWCClient } from "@getalby/sdk/nwc";

import dayjs from "dayjs";
import { ChevronUpIcon, MessageSquare, TriangleAlert, ZapIcon } from "lucide-react";
import React from "react";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import Loading from "src/components/Loading";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
} from "src/components/ui/dialog";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { Separator } from "src/components/ui/separator";
import { Textarea } from "src/components/ui/textarea";
import { PayInvoiceResponse } from "src/types";
import { request } from "src/utils/request";

import { useInfo } from "src/hooks/useInfo";

  // Must be a sub-wallet connection with only make invoice and list transactions permissions!

  
  type Message = {
    id: string;
    name?: string;
    message: string;
    amount: number;
    created_at: number;
  };
  
  type TabType = "latest" | "top";
  
  let nwcClient: NWCClient | undefined;
  function getNWCClient(nwcUrl: string): NWCClient {
    if (!nwcClient && nwcUrl) {
      nwcClient = new NWCClient({
        nostrWalletConnectUrl: nwcUrl,
      });
    }
    // This should ideally not happen if we guard before calling valid methods
    if (!nwcClient) {
      throw new Error("NWC Client not initialized");
    }
    return nwcClient;
  }
  
  function getSortedMessages(messages: Message[], tab: TabType): Message[] {
    if (tab === "latest") {
      return [...messages].sort((a, b) => b.created_at - a.created_at);
    } else {
      return [...messages].sort((a, b) => b.amount - a.amount);
    }
  }
  
  export function LightningMessageboardWidget() {
    const [messageText, setMessageText] = React.useState("");
    const [senderName, setSenderName] = React.useState("");
    const [amount, setAmount] = React.useState("");
    const [messages, setMessages] = React.useState<Message[]>();
    const [isLoading, setLoading] = React.useState(false);
    const [isSubmitting, setSubmitting] = React.useState(false);
    const [dialogOpen, setDialogOpen] = React.useState(false);
    const [isOpen, setOpen] = React.useState(false);
    const [currentTab, setCurrentTab] = React.useState<TabType>("latest");
    const [error, setError] = React.useState<string | undefined>();
    const { data: info } = useInfo();
    const messageboardNwcUrl = info?.messageboardNwcUrl;
    const enableMessageboardNwc = info?.enableMessageboardNwc ?? true;
  
    const isLoadingRef = React.useRef(false);
  
    const loadMessages = React.useCallback(() => {
      if (!messageboardNwcUrl || isLoadingRef.current) return;
      
      isLoadingRef.current = true;
      (async () => {
        // Only show spinner on initial load or manual retry, not background polling
        if (!messages) {
           setLoading(true);
        }
        setError(undefined);
        let offset = 0;
        
        // Use a Map to deduplicate messages by ID across pages
        const fetchedMessagesMap = new Map<string, Message>();
        
        while (true) {
          try {
            const transactions = await getNWCClient(
              messageboardNwcUrl
            ).listTransactions({
              offset,
              limit: 10,
            });
  
            if (transactions.transactions.length === 0) {
              break;
            }
  
            const newMessages = transactions.transactions.map((transaction) => ({
              id: transaction.payment_hash,
              created_at: transaction.created_at,
              message: transaction.description,
              name: (
                transaction.metadata as
                | { payer_data?: { name?: string } }
                | undefined
              )?.payer_data?.name as string | undefined,
              amount: Math.floor(transaction.amount / 1000),
            }));
  
            newMessages.forEach(msg => fetchedMessagesMap.set(msg.id, msg));
  
            // Update state incrementally, merging with existing messages map
            setMessages((prevMessages) => {
               // Create a map from previous messages to ensure we don't lose them if we are just verifying,
               // but here we are re-fetching from scratch (offset 0 loop), so technically we are building a new list.
               // However, to support "new messages" appearing, we might want to merge with existing?
               // The current logic fetches ALL history every time. This is safe but expensive.
               // We will replicate the "overwrite on offset 0" logic but using Map to be safe.
               
               // Actually, since we are fetching from offset 0, we can just build the map locally and set it?
               // The original code was updating incrementally.
               // Let's stick to simple:
               // If it's the first page (offset 0), we start fresh in the STATE perspective?
               // No, if we want to support "keep connection" (polling), we should MERGE.
               
               const currentMap = new Map();
               if (offset > 0 && prevMessages) {
                  prevMessages.forEach(m => currentMap.set(m.id, m));
               }
               newMessages.forEach(m => currentMap.set(m.id, m));
               return Array.from(currentMap.values());
            });
  
            offset += transactions.transactions.length;
          } catch (error) {
            console.error(error);
            setError((error as Error).message || "Failed to load messages");
            break;
          }
        }
        setLoading(false);
        isLoadingRef.current = false;
      })();
    }, [messageboardNwcUrl, messages]);

    React.useEffect(() => {
      const savedName = localStorage.getItem("messageboard_sender_name");
      if (savedName) setSenderName(savedName);
    }, []);

    React.useEffect(() => {
      if (senderName) {
        localStorage.setItem("messageboard_sender_name", senderName);
      }
    }, [senderName]);

    React.useEffect(() => {
      if (isOpen) {
        loadMessages();
        const interval = setInterval(loadMessages, 30000); // Poll every 30s
        return () => clearInterval(interval);
      }
    }, [isOpen, loadMessages]);
  
    const sortedMessages = React.useMemo(
      () => getSortedMessages(messages || [], currentTab),
      [currentTab, messages]
    );
  
    function handleSubmitOpenDialog(e: React.FormEvent) {
      e.preventDefault();
      setDialogOpen(true);
    }
    async function handleSubmit(e: React.FormEvent) {
      e.preventDefault();
  
      if (+amount < 1000) {
        toast.error("Amount too low", {
          description: "Minimum payment is 1000 loki",
        });
        return;
      }
  
      const amountMloki = +amount * 1000;
      setSubmitting(true);
      try {
        const transaction = await getNWCClient(messageboardNwcUrl!).makeInvoice({
          amount: amountMloki,
          description: messageText,

          metadata: {
            payer_data: {
              name: senderName,
            },
          },
        });
  
        const payInvoiceResponse = await request<PayInvoiceResponse>(
          `/api/payments/${transaction.invoice}`,
          {
            method: "POST",
          }
        );
        if (!payInvoiceResponse?.preimage) {
          throw new Error("No preimage in response");
        }
  
        setMessageText("");
        loadMessages();
        toast("Successfully sent message");
        setDialogOpen(false);
      } catch (error) {
        console.error(error);
        toast.error("Something went wrong", {
          description: "" + error,
        });
      }
      setSubmitting(false);
    }
  
    const topPlace = Math.max(
      1000,
      ...(messages?.map((message) => message.amount + 1) || [])
    );
    
    if (!enableMessageboardNwc) {
      return (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <MessageSquare className="w-5 h-5" />
              Lightning Messageboard
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground">
              Messageboard is disabled in settings.
            </p>
          </CardContent>
        </Card>
      );
    }
  
    if (!messageboardNwcUrl) {
      return (
        <Card className="border-destructive/50">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-destructive">
              <TriangleAlert className="w-5 h-5" />
              Configuration Error
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground">
              Messageboard is enabled but no NWC URL is configured. Please check your settings.
          </p>
          <Button asChild className="mt-4" variant="outline">
            <Link to="/settings">
              Go to Settings
            </Link>
          </Button>
          </CardContent>
        </Card>
      );
    }
  
    if (error) {
      return (
        <Card className="border-destructive/50">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-destructive">
              <TriangleAlert className="w-5 h-5" />
              Messageboard Error
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground">
              {error}
            </p>
            <div className="flex gap-2 mt-4">
              <Button variant="outline" onClick={loadMessages}>
                Retry
              </Button>
              <Button asChild variant="outline">
                <Link to="/settings">
                  Check Settings
                </Link>
              </Button>
            </div>
          </CardContent>
        </Card>
      );
    }

    return (
      <>
        <Card>
          <CardHeader>
            <div className="flex justify-between items-center">
              <CardTitle className="flex items-center gap-2">
                Lightning Messageboard{isLoading && <Loading />}
              </CardTitle>
              <Button variant="secondary" onClick={() => setOpen(!isOpen)}>
                {isOpen ? "Hide" : "Show"}
              </Button>
            </div>
          </CardHeader>
          {isOpen && (
            <CardContent>
              <div className="flex gap-2 mb-4 -mt-4">
                <Button
                  variant={currentTab === "latest" ? "default" : "outline"}
                  size="sm"
                  onClick={() => setCurrentTab("latest")}
                >
                  Latest
                </Button>
                <Button
                  variant={currentTab === "top" ? "default" : "outline"}
                  size="sm"
                  onClick={() => setCurrentTab("top")}
                >
                  Top
                </Button>
              </div>
              <div className="h-96 overflow-y-visible flex flex-col gap-2 overflow-hidden">
                {sortedMessages.map((message, index) => (
                  <div key={message.id}>
                    <CardHeader>
                      <CardTitle className="leading-6 break-anywhere">
                        {message.message}
                      </CardTitle>
                    </CardHeader>
                    <CardFooter className="flex items-center justify-between text-sm pb-2">
                      <CardTitle className="break-all font-normal text-xs">
                        <span className="text-muted-foreground">by</span>{" "}
                        {message.name || "Anonymous"}{" "}
                        <span className="text-muted-foreground">
                          {dayjs(message.created_at * 1000).fromNow()}
                        </span>
                      </CardTitle>
                      <div>
                        <Badge>
                          <ZapIcon />
                          <FormattedFlokicoinAmount
                            amount={message.amount * 1000}
                          />
                        </Badge>
                      </div>
                    </CardFooter>
                    {index !== sortedMessages.length - 1 && <Separator />}
                  </div>
                ))}
              </div>
              <form
                onSubmit={handleSubmitOpenDialog}
                className="flex items-center gap-2 mt-4"
              >
                <Input
                  required
                  placeholder="Type your message..."
                  value={messageText}
                  maxLength={140}
                  onChange={(e) => setMessageText(e.target.value)}
                />
                <Button>
                  <ZapIcon />
                  Send
                </Button>
              </form>
            </CardContent>
          )}
        </Card>
        <Dialog onOpenChange={setDialogOpen} open={dialogOpen}>
          <DialogContent className="sm:max-w-[600px]">
            <form onSubmit={handleSubmit}>
              <DialogHeader>
                <DialogTitle>Post Message</DialogTitle>
                <DialogDescription>
                  Pay to post on the Lokihub message board. The messages with the
                  highest number of loki will be shown first.
                </DialogDescription>
              </DialogHeader>
  
              <div className="grid gap-4 py-4">
                <div className="grid grid-cols-4 items-center gap-4">
                  <Label htmlFor="comment" className="text-right">
                    Your Name
                  </Label>
                  <div className="col-span-3">
                    <Input
                      id="sender-name"
                      value={senderName}
                      onChange={(e) => setSenderName(e.target.value)}
                      maxLength={20}
                      autoFocus
                    />
                  </div>
                </div>
  
                <div className="grid grid-cols-4 items-center gap-4">
                  <Label htmlFor="amount" className="text-right">
                    Amount (loki)
                  </Label>
                  <div className="col-span-2">
                    <Input
                      id="amount"
                      required
                      value={amount}
                      onChange={(e) => setAmount(e.target.value)}
                    />
                  </div>
                  <Button
                    type="button"
                    variant="secondary"
                    onClick={() => setAmount("" + topPlace)}
                  >
                    <ChevronUpIcon />
                    Top
                  </Button>
                </div>
                <div className="grid grid-cols-4 gap-4">
                  <Label htmlFor="comment" className="text-right pt-2">
                    Message
                  </Label>
                  <Textarea
                    id="comment"
                    value={messageText}
                    onChange={(e) => setMessageText(e.target.value)}
                    className="col-span-3"
                    rows={4}
                  />
                </div>
              </div>
              <DialogFooter>
                <LoadingButton
                  type="submit"
                  disabled={!!isSubmitting}
                  loading={isSubmitting}
                >
                  Confirm Payment
                </LoadingButton>
              </DialogFooter>
            </form>
          </DialogContent>
        </Dialog>
      </>
    );
  }
