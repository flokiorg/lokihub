import { AlertCircle, Check, ChevronsUpDown, Globe, Settings2, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "src/components/ui/button";
import {
    Command,
    CommandEmpty,
    CommandGroup,
    CommandInput,
    CommandItem,
    CommandList,
    CommandSeparator,
} from "src/components/ui/command";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import {
    Popover,
    PopoverContent,
    PopoverTrigger,
} from "src/components/ui/popover";

export interface ServiceOption {
  name: string;
  value: string;
  description?: string;
  recommended?: boolean;
}

interface ServiceSelectorProps {
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: ServiceOption[];
  placeholder?: string;
  description?: string;
  disabled?: boolean;
}

export function ServiceSelector({
  label,
  value,
  onChange,
  options,
  placeholder,
  description,
  disabled,
}: ServiceSelectorProps) {
  const [open, setOpen] = useState(false);
  const [isCustom, setIsCustom] = useState(false);

  // Check if current value matches any option
  useEffect(() => {
    const matched = options.some((opt) => opt.value === value);
    if (!matched && value) {
      setIsCustom(true);
    } else if (matched) {
      setIsCustom(false);
    }
  }, [value, options]);

  const selectedOption = options.find((opt) => opt.value === value);

  const getHostname = (url: string) => {
    try {
      return new URL(url).hostname;
    } catch {
      return url;
    }
  };

  return (
    <div className="grid gap-3">
      <div className="flex flex-col gap-1">
        <Label className="text-base font-semibold text-foreground/90">{label}</Label>
        {description && (
            <p className="text-sm text-muted-foreground leading-relaxed">{description}</p>
        )}
      </div>
      
      {!isCustom ? (
        <Popover open={open} onOpenChange={setOpen}>
          <PopoverTrigger asChild>
            <Button
              variant="outline"
              role="combobox"
              aria-expanded={open}
              className="w-full justify-between h-auto py-4 px-4 text-left border-muted-foreground/20 hover:border-primary/50 transition-colors"
              disabled={disabled}
            >
              {selectedOption ? (
                <div className="flex flex-col gap-1 overflow-hidden">
                  <div className="flex items-center gap-2">
                    <Globe className="w-4 h-4 text-primary shrink-0" />
                    <span className="font-semibold text-foreground">{selectedOption.name}</span>
                    {selectedOption.recommended && (
                         <span className="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded-full bg-primary/10 text-[10px] font-medium text-primary">
                            <ShieldCheck className="w-3 h-3" />
                            Verified
                         </span>
                    )}
                  </div>
                  <span className="text-xs text-muted-foreground font-mono truncate">
                    {getHostname(selectedOption.value)}
                  </span>
                </div>
              ) : (
                <span className="text-muted-foreground">{placeholder || "Select a service provider..."}</span>
              )}
              <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
            </Button>
          </PopoverTrigger>
          <PopoverContent className="w-[var(--radix-popover-trigger-width)] p-0" align="start">
            <Command>
              <CommandInput placeholder="Search providers..." />
              <CommandList>
                <CommandEmpty>No provider found.</CommandEmpty>
                <CommandGroup heading="Community Options">
                  {options.map((option) => (
                    <CommandItem
                      key={option.value}
                      value={option.name} // searching by name
                      onSelect={() => {
                        onChange(option.value);
                        setOpen(false);
                      }}
                      className="flex flex-col items-start py-3 px-4 gap-1 cursor-pointer aria-selected:bg-muted/50"
                    >
                      <div className="flex items-center w-full gap-2">
                        <span className="font-medium text-sm">{option.name}</span>
                        {option.recommended && (
                            <ShieldCheck className="w-3 h-3 text-primary/80" />
                        )}
                        {value === option.value && (
                          <Check className="ml-auto h-4 w-4 text-primary" />
                        )}
                      </div>
                      {option.description && (
                        <span className="text-xs text-muted-foreground/80 line-clamp-2 leading-snug">{option.description}</span>
                      )}
                      <span className="text-[10px] text-muted-foreground/50 font-mono truncate w-full pt-1">
                        {option.value}
                      </span>
                    </CommandItem>
                  ))}
                </CommandGroup>
                <CommandSeparator />
                <CommandGroup>
                  <CommandItem
                    onSelect={() => {
                        setOpen(false);
                        setIsCustom(true);
                    }}
                    className="flex items-center gap-2 py-3 px-4 cursor-pointer text-muted-foreground hover:text-foreground"
                  >
                    <Settings2 className="w-4 h-4" />
                    <span className="font-medium">Enter Custom URL</span>
                  </CommandItem>
                </CommandGroup>
              </CommandList>
            </Command>
          </PopoverContent>
        </Popover>
      ) : (
        <div className="flex gap-2 items-start animate-in fade-in slide-in-from-top-1 duration-200">
            <div className="flex-1 space-y-2">
                <div className="relative">
                    <Input 
                        value={value} 
                        onChange={(e) => onChange(e.target.value)} 
                        placeholder={placeholder || "https://..."}
                        disabled={disabled}
                        autoFocus
                        className="font-mono text-sm"
                    />
                    <div className="absolute right-3 top-2.5 text-xs text-muted-foreground">Custom</div>
                </div>
                 <p className="text-xs text-yellow-500/80 flex items-center gap-1.5 bg-yellow-500/10 p-2 rounded-md border border-yellow-500/20">
                    <AlertCircle className="w-3 h-3" />
                    Using a custom service requires trust in the provider.
                </p>
            </div>
            <Button 
                variant="ghost" 
                size="icon" 
                onClick={() => setIsCustom(false)}
                className="shrink-0 text-muted-foreground hover:text-foreground h-10 w-10 border border-transparent hover:border-input"
                title="Cancel Custom Input"
            >
                <span className="sr-only">Cancel</span>
                <Settings2 className="w-4 h-4 rotate-45" />
            </Button>
        </div>
      )}
    </div>
  );
}
