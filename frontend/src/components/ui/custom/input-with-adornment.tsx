import * as React from "react";
import { Input } from "src/components/ui/input";
import { cn } from "src/lib/utils";

export interface InputWithAdornmentProps extends React.ComponentProps<"input"> {
  endAdornment: React.ReactNode;
}

const InputWithAdornment = React.forwardRef<
  HTMLInputElement,
  InputWithAdornmentProps
>(({ className, type, endAdornment, dir, ...props }, ref) => {
  return (
    <div className="relative flex items-center w-full" dir={dir}>
      <Input
        type={type}
        ref={ref}
        dir={dir}
        className={cn(
          "[appearance:textfield] [&::-webkit-outer-spin-button]:appearance-none [&::-webkit-inner-spin-button]:appearance-none",
          endAdornment && (dir === "ltr" ? "pe-8 rtl:pe-0 rtl:ps-8" : "pe-8"),
          className
        )}
        {...props}
      />
      {endAdornment && (
        <span className="absolute end-1 flex items-center">
          {endAdornment}
        </span>
      )}
    </div>
  );
});

InputWithAdornment.displayName = "InputWithAdornment";

export { InputWithAdornment };
