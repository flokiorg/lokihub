"use client";

import { Command, SearchIcon } from "lucide-react";
import React from "react";
import { Badge } from "src/components/ui/badge";

import { Input } from "src/components/ui/input";
import { useCommandPaletteContext } from "src/contexts/CommandPaletteContext";
import { useLocale } from "src/hooks/useLocale";
import { cn } from "src/lib/utils";

interface SearchInputProps {
  placeholder?: string;
  className?: string;
}

export function SearchInput({
  placeholder = "Search pages, apps, etc...",
  className,
}: SearchInputProps) {
  const { setOpen } = useCommandPaletteContext();
  const { isRTL } = useLocale();

  const handleClick = React.useCallback(() => {
    setOpen(true);
  }, [setOpen]);

  const handleKeyDown = React.useCallback(
    (e: React.KeyboardEvent<HTMLInputElement>) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        setOpen(true);
      }
    },
    [setOpen]
  );

  return (
    <div
      className={cn("relative cursor-pointer", className)}
      onClick={handleClick}
    >
      <Input
        placeholder={placeholder}
        readOnly
        className="cursor-pointer ps-8 pe-20 max-sm:w-32"
        onKeyDown={handleKeyDown}
        tabIndex={0}
      />
      <SearchIcon className="absolute start-2 top-1/2 transform -translate-y-1/2 h-4 w-4 text-muted-foreground pointer-events-none" />
      <Badge
        variant="secondary"
        dir="ltr"
        className={cn(
          "absolute top-1/2 transform -translate-y-1/2 max-sm:hidden",
          isRTL ? "left-2" : "right-2"
        )}
      >
        <Command />K
      </Badge>
    </div>
  );
}
