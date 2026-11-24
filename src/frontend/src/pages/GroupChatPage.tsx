import ChatTopBar from "@/components/chat/ChatTopBar";
import Composer from "@/components/chat/Composer";
import ControlPanel from "@/components/chat/ControlPanel";
import GroupInfoSheet from "@/components/chat/GroupInfoSheet";
import GroupMembersPanel from "@/components/chat/GroupMembersPanel";
import { KillSwitchDialog } from "@/components/chat/KillSwitchDialog";
import MessageStream from "@/components/chat/MessageStream";
import TurnStatusBar from "@/components/chat/TurnStatusBar";
import { useGroupControls } from "@/components/chat/useGroupControls";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { useGroupDetail } from "@/hooks/useAdminQueries";
import { useGroupEvents } from "@/hooks/useLiveEvents";
import type { GroupDetail } from "@/lib/api";
import { formatNumberAr, parseGroupId } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { MessageSquare, MessagesSquare, Users } from "lucide-react";
import { type ReactNode, useState } from "react";

function NotFound() {
  return (
    <div
      data-ocid="groupchat.not_found_state"
      className="flex flex-1 items-center justify-center px-6"
    >
      <EmptyState
        icon={<MessagesSquare />}
        title="لم يتم العثور على المجموعة"
        description="ربما تم حذفها أو أن الرابط غير صحيح."
        action={
          <Button asChild>
            <Link to="/admin" data-ocid="groupchat.home_link">
              العودة إلى المجموعات
            </Link>
          </Button>
        }
      />
    </div>
  );
}

function LoadingState() {
  return (
    <div
      data-ocid="groupchat.loading_state"
      aria-busy="true"
      className="flex flex-1 flex-col gap-4 p-4"
    >
      <Skeleton className="h-14 w-full rounded-lg" />
      <div className="flex min-h-0 flex-1 gap-4">
        <Skeleton className="hidden h-full w-60 rounded-lg xl:block" />
        <div className="flex flex-1 flex-col gap-3">
          <Skeleton className="h-32 w-full max-w-md self-center rounded-lg" />
          <Skeleton className="h-12 w-2/3 rounded-2xl" />
          <Skeleton className="h-12 w-1/2 self-end rounded-2xl" />
          <Skeleton className="h-12 w-3/5 rounded-2xl" />
        </div>
        <Skeleton className="hidden h-full w-72 rounded-lg xl:block" />
      </div>
    </div>
  );
}

function TabButton({
  active,
  onClick,
  icon,
  label,
  ocid,
}: {
  active: boolean;
  onClick: () => void;
  icon: ReactNode;
  label: string;
  ocid: string;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      data-ocid={ocid}
      onClick={onClick}
      className={cn(
        "inline-flex h-9 flex-1 items-center justify-center gap-1.5 rounded-full px-3 text-xs font-semibold transition-[background-color,color] duration-fast",
        active
          ? "bg-secondary text-foreground shadow-xs"
          : "text-muted-foreground hover:text-foreground",
      )}
    >
      {icon}
      {label}
    </button>
  );
}

function Chat({ detail, live }: { detail: GroupDetail; live: boolean }) {
  const [infoOpen, setInfoOpen] = useState(false);
  const [controlsOpen, setControlsOpen] = useState(false);
  const [mobileView, setMobileView] = useState<"chat" | "members">("chat");
  const controls = useGroupControls(detail.group.id, detail.group.status);
  const openInfo = () => setInfoOpen(true);

  return (
    <>
      <ChatTopBar
        detail={detail}
        controls={controls}
        connected={live}
        onOpenInfo={openInfo}
        onOpenControls={() => setControlsOpen(true)}
      />

      <div className="flex min-h-0 flex-1">
        {/* Roster column — start side (right in RTL). */}
        <aside className="hidden w-60 shrink-0 flex-col border-e border-border bg-surface/40 xl:flex">
          <GroupMembersPanel detail={detail} onOpenInfo={openInfo} />
        </aside>

        <div className="flex min-w-0 flex-1 flex-col">
          {/* Mobile / tablet tabs */}
          <div
            role="tablist"
            aria-label="عرض المجموعة"
            className="flex shrink-0 items-center gap-1 border-b border-border bg-surface/50 px-2 py-1.5 xl:hidden"
          >
            <TabButton
              active={mobileView === "chat"}
              onClick={() => setMobileView("chat")}
              icon={<MessageSquare aria-hidden="true" className="size-3.5" />}
              label="الدردشة"
              ocid="groupchat.tab_chat"
            />
            <TabButton
              active={mobileView === "members"}
              onClick={() => setMobileView("members")}
              icon={<Users aria-hidden="true" className="size-3.5" />}
              label={`الأعضاء (${formatNumberAr(detail.members.length)})`}
              ocid="groupchat.tab_members"
            />
            <button
              type="button"
              data-ocid="groupchat.tab_controls"
              onClick={() => setControlsOpen(true)}
              className="inline-flex h-9 flex-1 items-center justify-center gap-1.5 rounded-full px-3 text-xs font-semibold text-muted-foreground transition-colors duration-fast hover:text-foreground"
            >
              التحكم
            </button>
          </div>

          <div
            className={cn(
              "flex min-h-0 flex-1 flex-col",
              mobileView === "members" && "hidden xl:flex",
            )}
          >
            <MessageStream detail={detail} onOpenInfo={openInfo} />
            {controls.error ? (
              <p
                role="alert"
                data-ocid="groupchat.action_error_state"
                className="border-t border-destructive/25 bg-destructive/10 px-4 py-2 text-center text-xs font-medium text-destructive"
              >
                {controls.error}
              </p>
            ) : null}
            <TurnStatusBar detail={detail} controls={controls} />
            <Composer detail={detail} ended={controls.ended} />
          </div>
          {mobileView === "members" ? (
            <div className="flex min-h-0 flex-1 flex-col bg-surface/20 xl:hidden">
              <GroupMembersPanel detail={detail} onOpenInfo={openInfo} />
            </div>
          ) : null}
        </div>

        {/* Controls column — end side (left in RTL). */}
        <aside className="hidden w-72 shrink-0 flex-col border-s border-border bg-surface/40 xl:flex">
          <ControlPanel
            detail={detail}
            controls={controls}
            className="flex min-h-0 flex-1 flex-col"
          />
        </aside>
      </div>

      <GroupInfoSheet
        detail={detail}
        controls={controls}
        open={infoOpen}
        onOpenChange={setInfoOpen}
      />

      {/* Mobile: the controls column becomes a bottom sheet. */}
      <Sheet open={controlsOpen} onOpenChange={setControlsOpen}>
        <SheetContent
          side="bottom"
          dir="rtl"
          data-ocid="groupchat.controls_sheet"
          className="flex max-h-[88dvh] flex-col gap-0 rounded-t-xl border-x border-border bg-card p-0"
        >
          <SheetHeader className="border-b border-border-subtle pe-12">
            <SheetTitle className="font-display text-base">
              التحكم في المجموعة
            </SheetTitle>
            <SheetDescription className="truncate">
              {detail.group.name}
            </SheetDescription>
          </SheetHeader>
          <ControlPanel
            detail={detail}
            controls={controls}
            className="flex min-h-0 flex-1 flex-col"
          />
        </SheetContent>
      </Sheet>

      <KillSwitchDialog controls={controls} />
    </>
  );
}

export default function GroupChatPage({ id }: { id: string }) {
  const groupId = parseGroupId(id);
  const live = useGroupEvents(groupId);
  const { data, isLoading } = useGroupDetail(groupId, live);

  let body: ReactNode;
  if (groupId === null || (!isLoading && data === null)) {
    body = <NotFound />;
  } else if (!data) {
    body = <LoadingState />;
  } else {
    body = <Chat detail={data} live={live} />;
  }

  return (
    <div
      data-ocid="groupchat.page"
      dir="rtl"
      className="flex min-h-0 flex-1 flex-col"
    >
      {body}
    </div>
  );
}
