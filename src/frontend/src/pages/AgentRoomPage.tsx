import { PolicyBox } from "@/components/agent-room/PolicyBox";
import { RecentMessages } from "@/components/agent-room/RecentMessages";
import { ReplyForm } from "@/components/agent-room/ReplyForm";
import { SmartWaitButton } from "@/components/agent-room/SmartWaitButton";
import { StatusBox } from "@/components/agent-room/StatusBox";
import {
  deriveStatus,
  isWaitingStatus,
} from "@/components/agent-room/roomStatus";
import { useSmartWait } from "@/components/agent-room/useSmartWait";
import { Button } from "@/components/ui/button";
import { useRoomEvents } from "@/hooks/useLiveEvents";
import { useRoomState } from "@/hooks/useRoomQueries";

function PlainMessage({ children }: { children: React.ReactNode }) {
  return (
    <main
      data-ocid="room.page"
      className="mx-auto min-h-screen max-w-2xl bg-background px-4 py-8 text-foreground"
    >
      {children}
    </main>
  );
}

export default function AgentRoomPage({ token }: { token: string }) {
  const { data, isLoading, isError, refetch } = useRoomState(token, 0);
  const { waiting, stillWaiting, start, resetStill } = useSmartWait(refetch);
  // Live push when available; the 18s wait button remains the fallback path.
  useRoomEvents(token, () => {
    void refetch();
  });

  if (data === null) {
    return (
      <PlainMessage>
        <p
          data-ocid="room.error_state"
          className="font-mono text-xl font-extrabold"
        >
          Invalid link
        </p>
      </PlainMessage>
    );
  }

  if (!data) {
    if (isError) {
      return (
        <PlainMessage>
          <p
            data-ocid="room.error_state"
            className="font-mono text-base font-bold"
          >
            Could not load the room.
          </p>
          <Button
            type="button"
            data-ocid="room.retry_button"
            onClick={() => void refetch()}
            className="mt-4 h-12 rounded-none border-2 border-foreground bg-foreground px-6 font-extrabold uppercase tracking-wider text-background hover:bg-foreground/85"
          >
            Try Again
          </Button>
        </PlainMessage>
      );
    }
    return (
      <PlainMessage>
        <p
          data-ocid="room.loading_state"
          className="font-mono text-base font-bold"
          aria-busy={isLoading}
        >
          Loading...
        </p>
      </PlainMessage>
    );
  }

  const status = deriveStatus(data, stillWaiting);
  const showReply = status === "YOUR_TURN";
  const showWait = isWaitingStatus(status);

  return (
    <PlainMessage>
      <div className="space-y-4">
        <PolicyBox />
        <StatusBox room={data} status={status} />
        {showReply && (
          <ReplyForm
            key={data.turnSeq.toString()}
            token={token}
            turnSeq={data.turnSeq}
            onSubmitted={resetStill}
          />
        )}
        {showWait && <SmartWaitButton waiting={waiting} onWait={start} />}
        <RecentMessages room={data} />
      </div>
    </PlainMessage>
  );
}
