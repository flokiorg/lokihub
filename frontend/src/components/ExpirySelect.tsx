import dayjs from "dayjs";
import { CalendarIcon } from "lucide-react";
import React from "react";
import { Calendar } from "src/components/ui/calendar";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "src/components/ui/popover";
import { cn } from "src/lib/utils";
import { expiryOptions } from "src/types";

const daysFromNow = (date?: Date) => {
  if (!date) {
    return undefined;
  }
  const now = dayjs();
  const targetDate = dayjs(date);
  return targetDate.diff(now, "day");
};

interface ExpiryProps {
  value?: Date | undefined;
  onChange: (expiryDate?: Date) => void;
  label?: string;
  // maxDate disables any preset or custom date past it (and disables "Never"
  // entirely, since an unbounded expiry always exceeds a finite cap) — used
  // where a caller enforces its own ceiling (e.g. a JIT Hub's max_exp_secs).
  maxDate?: Date;
}

const ExpirySelect: React.FC<ExpiryProps> = ({
  value,
  onChange,
  label = "Connection expiration",
  maxDate,
}) => {
  const [expiryDays, setExpiryDays] = React.useState(daysFromNow(value));
  const [customExpiry, setCustomExpiry] = React.useState(() => {
    const _daysFromNow = daysFromNow(value);
    return _daysFromNow !== undefined
      ? !Object.values(expiryOptions)
          .filter((value) => value !== 0)
          .includes(_daysFromNow)
      : false;
  });
  return (
    <>
      <p className="font-medium text-sm mb-2">{label}</p>
      <div className="grid grid-cols-2 md:grid-cols-6 gap-2 text-xs">
        {Object.keys(expiryOptions).map((expiry) => {
          const days = expiryOptions[expiry];
          // days === 0 means "Never" — always exceeds any finite maxDate.
          const exceedsMax =
            maxDate !== undefined &&
            (days === 0 || dayjs().add(days, "day").endOf("day").isAfter(maxDate));
          return (
            <button
              type="button"
              key={expiry}
              disabled={exceedsMax}
              onClick={() => {
                setCustomExpiry(false);
                let date: Date | undefined;
                if (expiryOptions[expiry]) {
                  date = dayjs()
                    .add(expiryOptions[expiry], "day")
                    .endOf("day")
                    .toDate();
                }
                onChange(date);
                setExpiryDays(expiryOptions[expiry]);
              }}
              className={cn(
                "cursor-pointer rounded text-nowrap border-2 text-center p-4",
                exceedsMax && "cursor-not-allowed opacity-40",
                !customExpiry && expiryDays == expiryOptions[expiry]
                  ? "border-primary"
                  : "border-muted"
              )}
            >
              {expiry}
            </button>
          );
        })}
        <Popover>
          <PopoverTrigger asChild>
            <button
              onClick={() => {}}
              className={cn(
                "flex items-center justify-center md:col-span-2 cursor-pointer rounded text-nowrap border-2 p-4",
                customExpiry ? "border-primary" : "border-muted"
              )}
            >
              <CalendarIcon className="me-2 h-4 w-4" />
              <span className="truncate">
                {customExpiry && value
                  ? dayjs(value).format("DD MMMM YYYY")
                  : "Custom..."}
              </span>
            </button>
          </PopoverTrigger>
          <PopoverContent className="w-auto p-0">
            <Calendar
              mode="single"
              disabled={{
                before: new Date(),
                ...(maxDate ? { after: maxDate } : {}),
              }}
              selected={value}
              onSelect={(date?: Date) => {
                if (!date) {
                  return;
                }
                date.setHours(23, 59, 59);
                setCustomExpiry(true);
                onChange(date);
                setExpiryDays(daysFromNow(date));
              }}
              autoFocus
            />
          </PopoverContent>
        </Popover>
      </div>
    </>
  );
};

export default ExpirySelect;
