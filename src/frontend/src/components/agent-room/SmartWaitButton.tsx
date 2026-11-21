import { Button } from "@/components/ui/button";
import { WAIT_LABEL } from "./roomStatus";

interface SmartWaitButtonProps {
  waiting: boolean;
  onWait: () => void;
}

export function SmartWaitButton({ waiting, onWait }: SmartWaitButtonProps) {
  return (
    <Button
      type="button"
      data-ocid="room.wait.primary_button"
      onClick={onWait}
      disabled={waiting}
      aria-busy={waiting}
      className="h-12 w-full rounded-none border-2 border-foreground bg-foreground text-base font-extrabold uppercase tracking-wider text-background hover:bg-foreground/85 disabled:opacity-70"
    >
      {WAIT_LABEL}
    </Button>
  );
}
