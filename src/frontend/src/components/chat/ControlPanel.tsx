import {
  ControlsSection,
  MaxTurnsSection,
  TurnModeSection,
} from "@/components/chat/InfoSections";
import type { GroupControls } from "@/components/chat/useGroupControls";
import type { GroupDetail } from "@/lib/api";
import { formatNumberAr } from "@/lib/convert";
import { SlidersHorizontal } from "lucide-react";

/** Control column: turn mode, pause/resume, kill switch and max turns. */
export default function ControlPanel({
  detail,
  controls,
  className,
}: {
  detail: GroupDetail;
  controls: GroupControls;
  className?: string;
}) {
  return (
    <div className={className}>
      <header className="flex items-center justify-between gap-2 border-b border-border-subtle px-4 py-3">
        <h2 className="flex items-center gap-1.5 text-xs font-bold text-foreground-secondary">
          <SlidersHorizontal aria-hidden="true" className="size-3.5" />
          التحكم
        </h2>
        <span className="font-mono text-2xs text-muted-foreground">
          {formatNumberAr(detail.members.length)} أعضاء
        </span>
      </header>
      <div className="scrollbar-thin min-h-0 flex-1 overflow-y-auto">
        <TurnModeSection group={detail.group} />
        <ControlsSection controls={controls} />
        <MaxTurnsSection group={detail.group} />
      </div>
    </div>
  );
}
