import { RotateCw } from "lucide-react";
import React from "react";

type State = { error: Error | null };

export class ErrorBoundary extends React.Component<
  React.PropsWithChildren,
  State
> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    console.error("Unhandled render error", error, info.componentStack);
  }

  render() {
    if (this.state.error) {
      return (
        <div className="flex items-center justify-center min-h-screen p-4">
          <div className="text-center space-y-4 max-w-md">
            <p className="text-destructive font-semibold text-lg">
              An unexpected error occurred
            </p>
            <p className="text-sm text-muted-foreground font-mono break-all">
              {this.state.error.message}
            </p>
            <button
              className="underline text-sm"
              onClick={() => window.location.reload()}
            >
              <RotateCw className="w-4 h-4 inline me-1" />
              Reload App
            </button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}
