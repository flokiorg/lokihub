import { Check, Globe, Plus, Server, ShieldCheck } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Input } from "src/components/ui/input";
import { cn } from "src/lib/utils";

export interface ServiceOption {
  name: string;
  value: string;
  uri?: string; // For LSP services
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
  customLabel?: string;
  customIcon?: React.ReactNode;
  fullWidth?: boolean;
}

export function ServiceCardSelector({
  value,
  onChange,
  options = [],
  placeholder,
  disabled,
  onBlur,
  onOptionSelect,
  customLabel,
  customIcon,
  fullWidth,
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
    <div className={cn("grid gap-4", fullWidth ? "grid-cols-1" : "grid-cols-[repeat(auto-fill,minmax(260px,1fr))]")}>
      {options.map((option) => {
        const isSelected = value === option.value;

        return (
          <div
            key={option.value}
            onClick={() => handleSelectOption(option.value)}
            className={cn(
              "relative group flex flex-col gap-2 p-3 rounded-lg border transition-all duration-200 cursor-pointer text-left h-full overflow-hidden",
              isSelected
                ? "border-primary ring-1 ring-primary shadow-sm"
                : "border-border hover:border-primary hover:shadow-sm",
              disabled && "opacity-50 pointer-events-none"
            )}
          >
            {/* Background Layer for older browser compatibility */}
            <div 
              className={cn(
                "absolute inset-0 transition-opacity duration-200 pointer-events-none",
                isSelected ? "bg-primary opacity-5" : "bg-muted opacity-0 group-hover:opacity-30"
              )} 
            />

            <div className="relative z-10 flex flex-col gap-2 h-full">
            <div className="flex items-start justify-between">
              <div className="flex items-center gap-2">
                  <div className={cn("relative p-1.5 rounded-md shrink-0 overflow-hidden", !isSelected && "bg-muted text-muted-foreground group-hover:text-foreground")}>
                      {isSelected && <div className="absolute inset-0 bg-primary opacity-10" />}
                      <div className={cn("relative z-10", isSelected && "text-primary")}>
                        {option.recommended ? <ShieldCheck className="w-4 h-4"/> : <Globe className="w-4 h-4" />}
                      </div>
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
                 <p className="text-[10px] text-muted-foreground opacity-50 font-mono truncate">
                    {getHostname(option.value)}
                 </p>
            </div>
            </div>
          </div>
        );
      })}

      {/* Custom Option Card */}
      <div
        onClick={handleSelectCustom}
        className={cn(
          "relative flex flex-col p-3 rounded-lg border border-dashed transition-all duration-200 cursor-pointer text-left h-full min-h-[110px] overflow-hidden group",
          isCustom
            ? "border-yellow-500 ring-1 ring-yellow-500 shadow-sm bg-card"
            : "border-border hover:border-primary hover:shadow-sm bg-transparent",
          disabled && "opacity-50 pointer-events-none"
        )}
      >
        <div className="relative z-10 flex flex-col h-full">
         {!isCustom ? (
            <div className="flex flex-col items-center justify-center h-full gap-2 text-center py-2 text-muted-foreground group-hover:text-primary transition-colors">
                <div className="p-2 rounded-full bg-muted/50 group-hover:bg-primary/10 group-hover:scale-110 transition-all duration-300">
                    {customIcon || <Plus className="w-5 h-5" />}
                </div>
                <div className="space-y-0.5">
                    <span className="font-medium text-sm block">{customLabel || "Add Custom Service"}</span>
                    <p className="text-[10px] text-muted-foreground leading-tight">Connect a trusted service</p>
                </div>
            </div>
         ) : (
            <>
                <div className="flex items-center justify-between mb-2">
                    <div className="flex items-center gap-2">
                         <div className="relative p-1.5 rounded-md overflow-hidden">
                             <div className="absolute inset-0 bg-yellow-500 opacity-10" />
                             <div className="relative z-10 text-yellow-600 dark:text-yellow-400">
                                <Server className="w-4 h-4" />
                             </div>
                         </div>
                         <span className="font-semibold text-sm">{customLabel || "Custom Service"}</span>
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
                        className="h-8 text-xs font-mono bg-background border-yellow-500 focus-visible:ring-yellow-500 px-2"
                        onClick={(e) => e.stopPropagation()} 
                         onBlur={onBlur}
                    />
                </div>
            </>
         )}
        </div>
      </div>

    </div>
  );
}
