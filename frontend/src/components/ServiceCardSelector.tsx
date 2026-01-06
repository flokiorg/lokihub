import { Check, Globe, PenLine, Server, ShieldCheck } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Input } from "src/components/ui/input";
import { cn } from "src/lib/utils";

export interface ServiceOption {
  name: string;
  value: string;
  description?: string;
  recommended?: boolean;
}

interface ServiceCardSelectorProps {
  value: string;
  onChange: (value: string) => void;
  options: ServiceOption[];
  placeholder?: string;
  disabled?: boolean;
  onBlur?: () => void;
  onOptionSelect?: (value: string) => void;
}

export function ServiceCardSelector({
  value,
  onChange,
  options = [],
  placeholder,
  disabled,
  onBlur,
  onOptionSelect,
}: ServiceCardSelectorProps) {
  const [isCustom, setIsCustom] = useState(false);
  const customInputRef = useRef<HTMLInputElement>(null);

  // Determine if the current value matches a predefined option
  useEffect(() => {
    const matched = options.some((opt) => opt.value === value);
    if (!matched && value) {
      setIsCustom(true);
    } else if (matched) {
      setIsCustom(false);
    }
  }, [value, options]);

  const handleSelectOption = (optValue: string) => {
    if (disabled) return;
    setIsCustom(false);
    onChange(optValue);
    if (onOptionSelect) {
        onOptionSelect(optValue);
    }
  };

  const handleSelectCustom = () => {
    if (disabled) return;
    setIsCustom(true);
    // If switching to custom, and value was one of the options, maybe clear it?
    // Or keep it as a starting point? User usually wants to enter something new.
    // Let's clear it if it matches an existing option, otherwise keep it.
    const matched = options.some((opt) => opt.value === value);
    if (matched) {
        onChange("");
    }
    setTimeout(() => customInputRef.current?.focus(), 50);
  };

  const getHostname = (url: string) => {
    try {
      if (!url) return "";
      return new URL(url).hostname;
    } catch {
      return url;
    }
  };

  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(260px,1fr))] gap-4">
      {options.map((option) => {
        const isSelected = value === option.value;

        return (
          <div
            key={option.value}
            onClick={() => handleSelectOption(option.value)}
            className={cn(
              "relative group flex flex-col gap-2 p-3 rounded-lg border transition-all duration-200 cursor-pointer text-left h-full",
              isSelected
                ? "border-primary bg-primary/5 ring-1 ring-primary shadow-sm"
                : "border-border hover:border-primary/50 hover:bg-muted/30 hover:shadow-sm",
              disabled && "opacity-50 pointer-events-none"
            )}
          >
            <div className="flex items-start justify-between">
              <div className="flex items-center gap-2">
                  <div className={cn("p-1.5 rounded-md shrink-0", isSelected ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground group-hover:text-foreground")}>
                      {option.recommended ? <ShieldCheck className="w-4 h-4"/> : <Globe className="w-4 h-4" />}
                  </div>
                  <span className="font-semibold text-sm leading-tight">{option.name}</span>
              </div>
              {isSelected && (
                <div className="bg-primary text-primary-foreground rounded-full p-0.5 shrink-0">
                  <Check className="w-3 h-3" />
                </div>
              )}
            </div>
            
            <div className="flex flex-col gap-1.5 mt-0.5">
                {option.description && (
                     <p className="text-xs text-muted-foreground line-clamp-2 leading-snug">{option.description}</p>
                )}
                 <p className="text-[10px] text-muted-foreground/50 font-mono truncate">
                    {getHostname(option.value)}
                 </p>
            </div>
          </div>
        );
      })}

      {/* Custom Option Card */}
      <div
        onClick={handleSelectCustom}
        className={cn(
          "relative flex flex-col p-3 rounded-lg border transition-all duration-200 cursor-pointer text-left h-full min-h-[110px]",
          isCustom
            ? "border-yellow-500/50 bg-yellow-500/5 ring-1 ring-yellow-500/20 shadow-sm"
            : "border-border hover:border-primary/50 hover:bg-muted/30 hover:shadow-sm",
          disabled && "opacity-50 pointer-events-none"
        )}
      >
         {!isCustom ? (
            <div className="flex flex-col items-center justify-center h-full gap-2 text-center py-2">
                <div className="p-2 rounded-full bg-muted/50 text-muted-foreground group-hover:text-primary group-hover:bg-primary/10 transition-colors">
                    <PenLine className="w-5 h-5" />
                </div>
                <div className="space-y-0.5">
                    <span className="font-medium text-sm block">Custom Service</span>
                    <p className="text-[10px] text-muted-foreground leading-tight">Connect a trusted service</p>
                </div>
            </div>
         ) : (
            <>
                <div className="flex items-center justify-between mb-2">
                    <div className="flex items-center gap-2">
                         <div className="p-1.5 rounded-md bg-yellow-500/10 text-yellow-600 dark:text-yellow-400">
                             <Server className="w-4 h-4" />
                         </div>
                         <span className="font-semibold text-sm">Custom URL</span>
                    </div>
                    {isCustom && (
                        <div className="bg-yellow-500 text-white rounded-full p-0.5">
                          <Check className="w-3 h-3" />
                        </div>
                    )}
                </div>
                
                <div className="mt-auto pt-1">
                    <Input
                        ref={customInputRef}
                        value={value}
                        onChange={(e) => onChange(e.target.value)}
                        placeholder={placeholder || "https://example.com"}
                        className="h-8 text-xs font-mono bg-background/50 border-yellow-500/20 focus-visible:ring-yellow-500/30 px-2"
                        onClick={(e) => e.stopPropagation()} 
                         onBlur={onBlur}
                    />
                </div>
            </>
         )}
      </div>

    </div>
  );
}
