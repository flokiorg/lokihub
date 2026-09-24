import React from "react";
import { ResponsiveContainer } from "recharts";
import { cn } from "src/lib/utils";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";

// One card per chart.
//
// The charts used to render bare inside a single "Analytics" card, four plots
// sharing one box. That reads as one crowded object rather than four separate
// readings, and leaves nowhere to put the sentence that says what a plot is
// for — so every chart carried an unexplained title and the reader had to
// infer the rest. A card each gives every plot a heading, a one-line
// description and its own edge, which is the pattern the rest of the app
// already uses for anything a user is meant to read and act on.
//
// The outer section is deliberately NOT a card any more: a card inside a card
// just draws a second border around the same content.
export function ChartCard({
  title,
  description,
  action,
  className,
  children,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  // Top-right slot for a status the reader should see without hovering —
  // a coverage ratio, a warning.
  action?: React.ReactNode;
  // Grid placement, e.g. "lg:col-span-2". A card whose content is a few
  // figures rather than a plot is given the full row instead of being paired
  // with one: a 200px plot beside a short summary leaves the summary sitting
  // in a half-empty box, which is the ragged look this grid had.
  className?: string;
  children: React.ReactNode;
}) {
  return (
    // h-full + column flex so a card fills its grid cell. Without it every
    // card keeps its natural height, and a short one (a coverage bar, say)
    // next to a 200px plot leaves a band of empty card below it — the
    // ragged, half-empty look this grid had.
    <Card className={cn("slashed-zero flex h-full flex-col", className)}>
      <CardHeader className="pb-2">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <CardTitle className="text-base">{title}</CardTitle>
            {description && (
              <CardDescription className="mt-1 text-xs">
                {description}
              </CardDescription>
            )}
          </div>
          {action && <div className="shrink-0">{action}</div>}
        </div>
      </CardHeader>
      {/* flex-1 takes the leftover height, and centring means short content
          sits in the middle of that space rather than pinned to the top with
          a void beneath it. */}
      <CardContent className="flex flex-1 flex-col justify-center">
        {children}
      </CardContent>
    </Card>
  );
}

// ChartCard's plot slot. Height is fixed rather than aspect-driven so that
// cards sitting side by side in a grid line up, whatever each plot contains.
export function ChartPlot({
  height = 200,
  children,
}: {
  height?: number;
  children: React.ReactElement;
}) {
  return (
    <ResponsiveContainer width="100%" height={height}>
      {children}
    </ResponsiveContainer>
  );
}
