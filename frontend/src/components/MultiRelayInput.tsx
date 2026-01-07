import { Check, Plus, X, Zap } from "lucide-react";
import { useState } from "react";
import { ServiceOption } from "src/components/ServiceCardSelector";
import { Button } from "src/components/ui/button";
import { Input } from "src/components/ui/input";
import { validateWebSocketURL } from "src/utils/validation";
// Removed Select components as they are no longer used
// import {
//     Select,
//     SelectContent,
//     SelectItem,
//     SelectTrigger,
//     SelectValue,
// } from "src/components/ui/select";

interface MultiRelayInputProps {
  value: string; // comma-separated relay URLs
  onChange: (value: string) => void;
  options?: ServiceOption[]; // community relay options
  placeholder?: string;
}

export function MultiRelayInput({
  value,
  onChange,
  options = [],
  placeholder = "wss://relay.example.com",
}: MultiRelayInputProps) {
  const [newRelay, setNewRelay] = useState("");
  // Removed selectedCommunityRelay state as it's no longer used
  // const [selectedCommunityRelay, setSelectedCommunityRelay] = useState("");
  const [error, setError] = useState("");

  // Convert comma-separated string to array, filter out empty strings
  const selectedRelays = value
    ? value.split(",").map((r) => r.trim()).filter((r) => r.length > 0)
    : [];

  // Separate community relays from custom relays
  const communityRelayUrls = options.map(opt => opt.value);
  const customRelays = selectedRelays.filter(url => !communityRelayUrls.includes(url));

  const validateRelay = (url: string): boolean => {
    const validationError = validateWebSocketURL(url, "Relay URL");
    if (validationError) {
      setError(validationError);
      return false;
    }
    if (selectedRelays.includes(url)) { // Changed from 'relays' to 'selectedRelays'
      setError("This relay is already added");
      return false;
    }
    setError("");
    return true;
  };

  // Removed addRelay function as its logic is now split between toggleCommunityRelay and addCustomRelay

  const toggleCommunityRelay = (relayUrl: string) => {
    let updatedRelays: string[];
    if (selectedRelays.includes(relayUrl)) {
      // Remove it
      updatedRelays = selectedRelays.filter(r => r !== relayUrl);
    } else {
      // Add it
      updatedRelays = [...selectedRelays, relayUrl];
    }
    onChange(updatedRelays.join(","));
  };

  const addCustomRelay = () => {
    const trimmedRelay = newRelay.trim();
    if (!trimmedRelay) return;

    if (validateRelay(trimmedRelay)) {
      const updatedRelays = [...selectedRelays, trimmedRelay];
      onChange(updatedRelays.join(","));
      setNewRelay("");
      setError(""); // Clear error on successful add
    }
  };

  // Removed addCommunityRelay function as its logic is now handled by toggleCommunityRelay

  const removeCustomRelay = (relayUrl: string) => { // Changed from removeRelay to removeCustomRelay
    const updatedRelays = selectedRelays.filter(r => r !== relayUrl);
    onChange(updatedRelays.join(","));
  };

  const handleKeyPress = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      addCustomRelay();
    }
  };

  return (
    <div className="space-y-3">

      {/* Community Relays - Selectable Cards */}
      {options.length > 0 && (
        <div className="space-y-2">
          <p className="text-sm font-medium">Community Relays</p>
          <div className="grid grid-cols-1 gap-3">
            {options.map((option, index) => {
              const isSelected = selectedRelays.includes(option.value);
              return (
                <div
                  key={index}
                  onClick={() => toggleCommunityRelay(option.value)}
                  className={`
                    relative flex items-start justify-between p-4 rounded-xl border transition-all cursor-pointer select-none
                    ${isSelected 
                      ? 'border-primary bg-primary/5 ring-1 ring-primary' 
                      : 'border-border hover:border-primary/50 hover:bg-muted/50'
                    }
                  `}
                >
                  <div className="flex gap-3">
                    <div className="flex-shrink-0 mt-0.5">
                       <div className={`p-2 rounded-lg ${isSelected ? 'bg-primary/20' : 'bg-muted'}`}>
                          <Zap className="w-4 h-4 text-primary" />
                       </div>
                    </div>
                    <div className="space-y-1 text-left">
                       <div className="font-semibold text-sm">{option.name || "Unknown Relay"}</div>
                       {option.description && (
                          <div className="text-sm text-muted-foreground">{option.description}</div>
                       )}
                       <div className="text-xs font-mono text-muted-foreground/80">{option.value}</div>
                    </div>
                  </div>
                  
                  {isSelected && (
                    <div className="flex-shrink-0 text-primary">
                      <div className="bg-primary text-primary-foreground rounded-full p-0.5">
                        <Check className="w-3 h-3" />
                      </div>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* Custom Relays - With Delete */}
      {customRelays.length > 0 && (
        <div className="space-y-2">
          <p className="text-sm font-medium">Custom Relays</p>
          <div className="space-y-2">
            {customRelays.map((relay, index) => (
              <div
                key={index}
                className="flex items-center gap-2 p-2 bg-muted rounded-md"
              >
                <span className="flex-1 text-sm font-mono break-all">
                  {relay}
                </span>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => removeCustomRelay(relay)} // Changed to removeCustomRelay
                  className="h-8 w-8 p-0"
                >
                  <X className="h-4 w-4" />
                </Button>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Add custom relay */}
      <div className="space-y-1">
        <p className="text-sm font-medium">Add Custom Relay</p>
        <div className="flex gap-2">
          <div className="flex-1">
            <Input
              type="text"
              value={newRelay}
              onChange={(e) => setNewRelay(e.target.value)}
              onKeyPress={handleKeyPress}
              placeholder={placeholder}
              className="font-mono text-sm"
            />
            {error && (
              <p className="text-sm text-destructive mt-1">{error}</p>
            )}
          </div>
          <Button
            type="button"
            variant="outline"
            onClick={addCustomRelay}
            className="shrink-0"
          >
            <Plus className="h-4 w-4 mr-2" />
            Add
          </Button>
        </div>
      </div>
    </div>
  );
}
