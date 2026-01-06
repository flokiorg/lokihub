import { Avatar, AvatarFallback, AvatarImage } from "src/components/ui/avatar";

import { cn } from "src/lib/utils";

function UserAvatar({ className }: { className?: string }) {
  return (
    <Avatar className={cn("h-8 w-8 rounded-lg", className)}>
      <AvatarImage src={undefined} alt="Avatar" />
      <AvatarFallback className="font-medium rounded-lg">
        LH
      </AvatarFallback>
    </Avatar>
  );
}

export default UserAvatar;
