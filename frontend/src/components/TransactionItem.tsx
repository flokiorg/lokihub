import dayjs from "dayjs";
import utc from "dayjs/plugin/utc";
import {
    ArrowDownIcon,
    ArrowDownUpIcon,
    ArrowUpDownIcon,
    ArrowUpIcon,
    ChevronDownIcon,
    ChevronUpIcon,
    CopyIcon,
    XIcon,
} from "lucide-react";
import { nip19 } from "nostr-tools";
import React from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import AppAvatar from "src/components/AppAvatar";
import ExternalLink from "src/components/ExternalLink";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { PaymentFailedAlert } from "src/components/PaymentFailedAlert";
import PodcastingInfo from "src/components/PodcastingInfo";
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
    DialogTrigger,
} from "src/components/ui/dialog";
import { LOKI_ACCOUNT_APP_NAME } from "src/constants";
import { useApp } from "src/hooks/useApp";
import { useSwap } from "src/hooks/useSwaps";
import { useLocale } from "src/hooks/useLocale";
import { copyToClipboard } from "src/lib/clipboard";
import { cn } from "src/lib/utils";
import { Transaction } from "src/types";

dayjs.extend(utc);

type Props = {
  tx: Transaction;
};

function safeNpubEncode(hex: string): string | undefined {
  try {
    return nip19.npubEncode(hex);
  } catch {
    return undefined;
  }
}

function TransactionItem({ tx }: Props) {
  const { t } = useTranslation("wallet");
  const { isRTL } = useLocale();
  const { data: app } = useApp(tx.appId);
  const swapId = tx.metadata?.swap_id;
  const { data: swap } = useSwap(swapId);
  const [showDetails, setShowDetails] = React.useState(false);
  const type = tx.type;

  const pubkey = tx.metadata?.nostr?.pubkey;
  const npub = pubkey ? safeNpubEncode(pubkey) : undefined;

  const payerName = tx.metadata?.payer_data?.name;
  const from =
    type === "incoming"
      ? payerName
        ? t("transactions.fromPayer", { name: payerName })
        : npub
          ? t("transactions.zapFrom", { npub: npub.substring(0, 12) + "..." })
          : swap
            ? t("transactions.swapFrom", { address: swap.lockupAddress })
            : undefined
      : undefined;

  const recipientIdentifier = tx.metadata?.recipient_data?.identifier;
  const to =
    type === "outgoing"
      ? npub
        ? t("transactions.zapTo", { npub: npub.substring(0, 12) + "..." })
        : swap?.type === "out"
          ? t("transactions.swapTo", { address: swap.destinationAddress })
          : recipientIdentifier
            ? t(
                tx.state === "failed"
                  ? "transactions.paymentTo"
                  : "transactions.toRecipient",
                { identifier: recipientIdentifier }
              )
            : undefined
      : undefined;

  const eventId = tx.metadata?.nostr?.tags?.find((t) => t[0] === "e")?.[1];

  const description = tx.description || tx.metadata?.comment;

  const typeStateText =
    type === "incoming"
      ? t("transactions.received")
      : tx.state === "settled"
        ? t("transactions.sent")
        : tx.state === "pending"
          ? t("transactions.sending")
          : t("transactions.failed");

  const dialogTitle =
    type === "incoming"
      ? t("transactions.receivedPayment")
      : tx.state === "settled"
        ? t("transactions.sentPayment")
        : tx.state === "pending"
          ? t("transactions.sendingPayment")
          : t("transactions.failedPayment");

  const Icon =
    tx.state === "failed"
      ? XIcon
      : tx.type === "outgoing"
        ? swapId
          ? ArrowUpDownIcon
          : ArrowUpIcon
        : swapId
          ? ArrowDownUpIcon
          : ArrowDownIcon;

  const copy = (text: string) => {
    copyToClipboard(text);
  };

  const typeStateIcon = (
    <div className="flex items-center">
      <div
        className={cn(
          "flex justify-center items-center rounded-full w-10 h-10 md:w-14 md:h-14 relative",
          tx.state === "failed"
            ? "bg-red-100 dark:bg-rose-950"
            : tx.state === "pending"
              ? "bg-blue-100 dark:bg-sky-950"
              : type === "outgoing"
                ? "bg-orange-100 dark:bg-amber-950"
                : "bg-green-100 dark:bg-emerald-950"
        )}
      >
        <Icon
          strokeWidth={3}
          className={cn(
            "size-6 md:w-8 md:h-8",
            tx.state === "failed"
              ? "stroke-red-500 dark:stroke-rose-500"
              : tx.state === "pending"
                ? "stroke-blue-500 dark:stroke-sky-500"
                : type === "outgoing"
                  ? "stroke-orange-500 dark:stroke-amber-500"
                  : "stroke-green-500 dark:stroke-teal-500"
          )}
        />
        {app && (
          <div
            className="absolute -bottom-1 -end-1"
            title={`${typeStateText} via ${app.name === LOKI_ACCOUNT_APP_NAME ? "Loki Account" : app.name}`}
          >
            <AppAvatar
              app={app}
              className="border-none p-0 rounded-full w-[18px] h-[18px] md:w-6 md:h-6 shadow-xs"
            />
          </div>
        )}
      </div>
    </div>
  );

  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) {
          setShowDetails(false);
        }
      }}
    >
      <DialogTrigger className="p-3 mb-4 hover:bg-muted/50 data-[state=selected]:bg-muted cursor-pointer rounded-md slashed-zero transaction sensitive">
        <div
          className={cn(
            "flex gap-3",
            tx.state === "pending" && "animate-pulse"
          )}
        >
          <div className={isRTL ? "order-3" : "order-1"}>
            {typeStateIcon}
          </div>
          <div
            className={cn(
              "overflow-hidden max-w-full flex flex-col justify-center",
              isRTL
                ? "order-2 ms-auto items-end"
                : "order-2 me-3 items-start"
            )}
          >
            <div className={cn("flex items-center gap-2", isRTL && "flex-row-reverse")}>
              <span className="md:text-xl font-semibold break-all line-clamp-1">
                {typeStateText}
                {from !== undefined && <>&nbsp;{from}</>}
                {to !== undefined && <>&nbsp;{to}</>}
              </span>
              <span className="text-xs md:text-base text-muted-foreground shrink-0" dir="ltr">
                {dayjs(tx.updatedAt).fromNow()}
              </span>
            </div>
            <p className="text-sm md:text-base text-muted-foreground break-all line-clamp-1">
              {description}
            </p>
          </div>
          <div
            className={cn(
              "flex shrink-0",
              isRTL ? "order-1" : "order-3 ms-auto"
            )}
          >
            <div
              className={cn(
                "flex flex-col md:text-xl",
                isRTL ? "items-start" : "items-end"
              )}
            >
              <p
                dir="ltr"
                className={cn(
                  type == "incoming" && "text-green-600 dark:text-emerald-500"
                )}
              >
                {type == "outgoing" ? "-" : "+"}
                <FormattedFlokicoinAmount
                  amount={tx.amount}
                  className="font-medium"
                />
              </p>
              <FormattedFiatAmount
                className="text-xs md:text-base"
                amount={Math.floor(tx.amount / 1000)}
              />
            </div>
          </div>
        </div>
      </DialogTrigger>
      <DialogContent className="slashed-zero">
        <DialogHeader>
          <DialogTitle
            className={cn(tx.state === "pending" && "animate-pulse")}
          >{dialogTitle}</DialogTitle>
          <DialogDescription className="text-start text-foreground max-h-[90vh] overflow-y-auto pe-2">
            <div
              className={cn(
                "flex items-center mt-6",
                tx.state === "pending" && "animate-pulse"
              )}
            >
              {typeStateIcon}
              <div className="ms-4">
                <p className="text-xl md:text-2xl font-semibold sensitive">
                  <FormattedFlokicoinAmount amount={tx.amount} />
                </p>
                <FormattedFiatAmount amount={Math.floor(tx.amount / 1000)} />
              </div>
            </div>
            {app && (
              <div className="mt-8">
                <p>{t("transactions.app")}</p>
                <Link to={`/apps/${app.id}`}>
                  <p className="font-semibold">
                    {app.name === LOKI_ACCOUNT_APP_NAME
                      ? "Loki Account"
                      : app.name}
                  </p>
                </Link>
              </div>
            )}
            {swapId && (
              <div className="mt-8">
                <p>{t("transactions.swapId")}</p>
                <Link
                  to={`/wallet/swap/${type === "incoming" ? "in" : "out"}/status/${swapId}`}
                  className="flex items-center gap-1"
                >
                  <p className="underline">{swapId}</p>
                </Link>
              </div>
            )}
            {to && (
              <div className="mt-6">
                <p>{t("transactions.to")}</p>
                <p className="text-muted-foreground">{to}</p>
              </div>
            )}
            {payerName && (
              <div className="mt-6">
                <p>{t("transactions.from")}</p>
                <p className="text-muted-foreground">{payerName}</p>
              </div>
            )}
            <div className="mt-6">
              <p>{t("transactions.dateTime")}</p>
              <p className="text-muted-foreground text-end" dir="ltr">
                {dayjs(tx.updatedAt).local().format("D MMMM YYYY, HH:mm")}
              </p>
            </div>
            {tx.state != "failed" && type == "outgoing" && (
              <div className="mt-6">
                <p>{t("transactions.fee")}</p>
                <p className="text-muted-foreground text-end" dir="ltr">
                  <FormattedFlokicoinAmount amount={tx.feesPaid} />
                  {tx.feesPaid > 0 && (
                    <>&nbsp;({((tx.feesPaid / tx.amount) * 100).toFixed(2)}%)</>
                  )}
                </p>
              </div>
            )}
            {tx.description && (
              <div className="mt-6">
                <p>{t("transactions.description")}</p>
                <p className="text-muted-foreground break-all">
                  {tx.description}
                </p>
              </div>
            )}
            {tx.metadata?.comment && (
              <div className="mt-6">
                <p>{t("transactions.comment")}</p>
                <p className="text-muted-foreground break-all">
                  {tx.metadata.comment}
                </p>
              </div>
            )}
            {tx.metadata?.nostr && eventId && npub && (
              <div className="mt-6">
                <p>
                  <ExternalLink
                    to={`https://njump.me/${nip19.neventEncode({
                      id: eventId,
                    })}`}
                    className="underline"
                  >
                    {t("transactions.nostrZap")}
                  </ExternalLink>{" "}
                  <span className="text-muted-foreground break-all">
                    {t("transactions.nostrZapFrom", { npub })}
                  </span>
                </p>
              </div>
            )}
            {tx.state === "failed" && (
              <div className="mt-6">
                <PaymentFailedAlert
                  errorMessage={tx.failureReason}
                  invoice={tx.invoice}
                />
              </div>
            )}
            <div className="mt-4 w-full">
              <div
                className="flex items-center gap-2 cursor-pointer"
                onClick={() => setShowDetails(!showDetails)}
              >
                {t("transactions.details")}
                {showDetails ? (
                  <ChevronUpIcon className="size-4" />
                ) : (
                  <ChevronDownIcon className="size-4" />
                )}
              </div>
              {showDetails && (
                <>
                  {tx.boostagram && <PodcastingInfo boost={tx.boostagram} />}
                  {tx.preimage && (
                    <div className="mt-6">
                      <p>{t("transactions.preimage")}</p>
                      <div className="flex items-center gap-4">
                        <p className="text-muted-foreground break-all" dir="ltr">
                          {tx.preimage}
                        </p>
                        <CopyIcon
                          className="cursor-pointer text-muted-foreground size-4 shrink-0"
                          onClick={() => {
                            if (tx.preimage) {
                              copy(tx.preimage);
                            }
                          }}
                        />
                      </div>
                    </div>
                  )}
                  <div className="mt-6">
                    <p>{t("transactions.hash")}</p>
                    <div className="flex items-center gap-4">
                      <p className="text-muted-foreground break-all" dir="ltr">
                        {tx.paymentHash}
                      </p>
                      <CopyIcon
                        className="cursor-pointer text-muted-foreground size-4 shrink-0"
                        onClick={() => {
                          copy(tx.paymentHash);
                        }}
                      />
                    </div>
                  </div>
                  <div className="mt-6">
                    <p>{t("transactions.invoice")}</p>
                    <div className="flex items-center gap-4">
                      <p className="text-muted-foreground break-all" dir="ltr">
                        {tx.invoice}
                      </p>
                      <CopyIcon
                        className="cursor-pointer text-muted-foreground size-4 shrink-0"
                        onClick={() => {
                          copy(tx.invoice);
                        }}
                      />
                    </div>
                  </div>
                  {!!tx.failureReason && (
                    <div className="mt-6">
                      <p>{t("transactions.failureReason")}</p>
                      <div className="flex items-center gap-4">
                        <p className="text-muted-foreground break-anywhere">
                          {tx.failureReason}
                        </p>
                        <CopyIcon
                          className="cursor-pointer text-muted-foreground size-4 shrink-0"
                          onClick={() => {
                            copy(tx.failureReason);
                          }}
                        />
                      </div>
                    </div>
                  )}
                  {tx.metadata && (
                    <div className="mt-6">
                      <p>{t("transactions.metadata")}</p>
                      <div className="flex items-center gap-4">
                        <p className="text-muted-foreground break-all" dir="ltr">
                          {JSON.stringify(tx.metadata)}
                        </p>
                        <CopyIcon
                          className="cursor-pointer text-muted-foreground size-4 shrink-0"
                          onClick={() => {
                            copy(JSON.stringify(tx.metadata));
                          }}
                        />
                      </div>
                    </div>
                  )}
                </>
              )}
            </div>
          </DialogDescription>
        </DialogHeader>
      </DialogContent>
    </Dialog>
  );
}

export default TransactionItem;
