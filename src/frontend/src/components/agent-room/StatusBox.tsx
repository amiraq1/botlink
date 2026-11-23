import type { RoomState } from "@/lib/api";
import {
  type RoomStatusKey,
  STATUS_ACTION,
  STATUS_HEADLINE,
  displayTurn,
} from "./roomStatus";

interface StatusBoxProps {
  room: RoomState;
  status: RoomStatusKey;
}

export function StatusBox({ room, status }: StatusBoxProps) {
  const turn = displayTurn(room, status);
  return (
    <section
      data-ocid="room.status.panel"
      aria-live="polite"
      className="border-2 border-foreground bg-card px-4 py-4"
    >
      <h1 className="font-mono text-xl font-extrabold leading-tight text-foreground md:text-2xl">
        {STATUS_HEADLINE[status]}
      </h1>
      <p className="mt-3 text-base font-semibold text-foreground">
        {STATUS_ACTION[status]}
      </p>
      <p
        data-ocid="room.status.meta"
        className="mt-3 whitespace-pre-wrap break-words font-mono text-sm text-foreground"
      >
        <>
          Group: <bdi>{room.groupName}</bdi> | You: <bdi>{room.botName}</bdi> (
          <bdi>{room.roleTitle}</bdi>) | Online:{" "}
          {room.onlineNames.map((name, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: member names may repeat; position is the only unique key
            <span key={`${name}-${i}`}>
              {i > 0 ? ", " : ""}
              <bdi>{name}</bdi>
            </span>
          ))}{" "}
          | Turn: {turn}/{room.maxTurns.toString()} | Topic:{" "}
          <bdi>{room.topic}</bdi>
        </>
      </p>
    </section>
  );
}
