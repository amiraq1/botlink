import AddMemberForm from "@/components/chat/AddMemberForm";
import {
  ControlsSection,
  MaxTurnsSection,
  Section,
  TurnModeSection,
} from "@/components/chat/InfoSections";
import MembersList from "@/components/chat/MembersList";
import ToolAuditLog from "@/components/chat/ToolAuditLog";
import type { GroupControls } from "@/components/chat/useGroupControls";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import type { GroupDetail } from "@/lib/api";
import { formatNumberAr } from "@/lib/convert";

/** Drawer with turn mode, controls, max turns, add-bot form, the members list, and tool audit. */
export default function GroupInfoSheet({
  detail,
  controls,
  open,
  onOpenChange,
}: {
  detail: GroupDetail;
  controls: GroupControls;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { group, members } = detail;
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="left"
        dir="rtl"
        data-ocid="groupinfo.sheet"
        className="w-full gap-0 border-s border-border bg-card p-0 sm:max-w-md"
      >
        <SheetHeader className="border-b border-border-subtle bg-surface/40 pe-14">
          <SheetTitle className="font-display text-base font-bold">
            معلومات المجموعة
          </SheetTitle>
          <SheetDescription className="truncate">{group.name}</SheetDescription>
        </SheetHeader>
        <div className="scrollbar-thin min-h-0 flex-1 overflow-y-auto">
          <TurnModeSection group={group} />
          <ControlsSection controls={controls} />
          <MaxTurnsSection group={group} />
          <Section title="إضافة عضو جديد">
            <AddMemberForm
              groupId={group.id}
              memberCount={members.length}
              usedColors={members.map((m) => m.accentColor.toUpperCase())}
            />
          </Section>
          <Section title={`الأعضاء (${formatNumberAr(members.length)})`}>
            <MembersList groupId={group.id} members={members} />
          </Section>
          <Section title="سجل استدعاء الأدوات">
            <ToolAuditLog groupId={group.id} />
          </Section>
        </div>
      </SheetContent>
    </Sheet>
  );
}
