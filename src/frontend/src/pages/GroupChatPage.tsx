import type { GroupDetail } from "@/backend";
import ChatTopBar from "@/components/chat/ChatTopBar";
import Composer from "@/components/chat/Composer";
import GroupInfoSheet from "@/components/chat/GroupInfoSheet";
import { KillSwitchDialog } from "@/components/chat/KillSwitchDialog";
import MessageStream from "@/components/chat/MessageStream";
import TurnStatusBar from "@/components/chat/TurnStatusBar";
import { useGroupControls } from "@/components/chat/useGroupControls";
import { Skeleton } from "@/components/ui/skeleton";
import { useGroupDetail } from "@/hooks/useAdminQueries";
import { parseGroupId } from "@/lib/convert";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";

function NotFound() {
  return (
    <div
      data-ocid="groupchat.not_found_state"
      className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center"
    >
      <p className="font-display text-xl font-bold">
        لم يتم العثور على المجموعة
      </p>
      <p className="text-sm text-muted-foreground">
        ربما تم حذفها أو أن الرابط غير صحيح.
      </p>
      <Link
        to="/admin"
        data-ocid="groupchat.home_link"
        className="inline-flex min-h-11 items-center rounded-full bg-primary px-5 text-sm font-bold text-primary-foreground focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
      >
        العودة إلى المجموعات
      </Link>
    </div>
  );
}

function LoadingState() {
  return (
    <div
      data-ocid="groupchat.loading_state"
      className="flex flex-1 flex-col gap-4 p-4"
    >
      <Skeleton className="h-14 w-full rounded-2xl" />
      <Skeleton className="h-40 w-full max-w-md self-center rounded-2xl" />
      <Skeleton className="h-12 w-2/3 rounded-2xl" />
      <Skeleton className="h-12 w-1/2 self-end rounded-2xl" />
    </div>
  );
}

function Chat({ detail }: { detail: GroupDetail }) {
  const [infoOpen, setInfoOpen] = useState(false);
  const controls = useGroupControls(detail.group.id, detail.group.status);
  const openInfo = () => setInfoOpen(true);

  return (
    <>
      <ChatTopBar detail={detail} controls={controls} onOpenInfo={openInfo} />
      <MessageStream detail={detail} onOpenInfo={openInfo} />
      {controls.error ? (
        <p
          role="alert"
          data-ocid="groupchat.action_error_state"
          className="bg-destructive/15 px-4 py-2 text-center text-xs text-destructive"
        >
          {controls.error}
        </p>
      ) : null}
      <TurnStatusBar detail={detail} controls={controls} />
      <Composer detail={detail} ended={controls.ended} />
      <GroupInfoSheet
        detail={detail}
        controls={controls}
        open={infoOpen}
        onOpenChange={setInfoOpen}
      />
      <KillSwitchDialog controls={controls} />
    </>
  );
}

export default function GroupChatPage({ id }: { id: string }) {
  const groupId = parseGroupId(id);
  const { data, isLoading } = useGroupDetail(groupId);

  let body: ReactNode;
  if (groupId === null || (!isLoading && data === null)) {
    body = <NotFound />;
  } else if (!data) {
    body = <LoadingState />;
  } else {
    body = <Chat detail={data} />;
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
