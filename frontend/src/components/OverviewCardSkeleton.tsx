import { Card, CardContent } from "src/components/ui/card";
import { Skeleton } from "src/components/ui/skeleton";

// Placeholder for CashHubOverview / CashHubsSummary / CircleHubOverview
// while their stats are still loading — same lead-pair-plus-breakdown-row
// shape all three share, so the card doesn't just pop into existence (or
// vanish entirely, which is what CashHubsSummary/CircleHubOverview's
// callers used to do while `stats` was undefined).
export function OverviewCardSkeleton() {
  return (
    <Card className="slashed-zero">
      <CardContent className="grid gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
        <div className="grid grid-cols-2 gap-4 border-t pt-4 sm:grid-cols-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      </CardContent>
    </Card>
  );
}
