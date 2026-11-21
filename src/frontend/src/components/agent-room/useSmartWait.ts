import type { RoomState } from "@/lib/api";
import { GroupStatus } from "@/lib/api";
import { useCallback, useEffect, useRef, useState } from "react";

const POLL_MS = 3000;
const MAX_POLLS = 6; // 6 x 3s = 18 seconds

type Refetch = () => Promise<{ data?: RoomState | null }>;

function isDecisive(room: RoomState | null | undefined): boolean {
  if (!room) return false;
  if (
    room.status === GroupStatus.completed ||
    room.status === GroupStatus.stopped
  ) {
    return true;
  }
  return room.status === GroupStatus.running && room.isYourTurn;
}

/** Polls room state every 3s for up to 18s. Stops on YOUR_TURN / ended. Timers are cleaned up on unmount. */
export function useSmartWait(refetch: Refetch) {
  const [waiting, setWaiting] = useState(false);
  const [stillWaiting, setStillWaiting] = useState(false);
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const refetchRef = useRef(refetch);
  refetchRef.current = refetch;

  const clearTimer = useCallback(() => {
    if (timerRef.current !== null) {
      clearInterval(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  useEffect(() => clearTimer, [clearTimer]);

  const start = useCallback(() => {
    if (timerRef.current !== null) return;
    setStillWaiting(false);
    setWaiting(true);
    let polls = 0;
    let busy = false;
    timerRef.current = setInterval(() => {
      polls += 1;
      const isLast = polls >= MAX_POLLS;
      if (busy) {
        if (isLast) {
          clearTimer();
          setWaiting(false);
          setStillWaiting(true);
        }
        return;
      }
      busy = true;
      refetchRef
        .current()
        .then((result) => {
          if (timerRef.current === null) return;
          if (isDecisive(result.data)) {
            clearTimer();
            setWaiting(false);
          } else if (isLast) {
            clearTimer();
            setWaiting(false);
            setStillWaiting(true);
          }
        })
        .catch(() => {
          if (timerRef.current === null || !isLast) return;
          clearTimer();
          setWaiting(false);
          setStillWaiting(true);
        })
        .finally(() => {
          busy = false;
        });
    }, POLL_MS);
  }, [clearTimer]);

  const resetStill = useCallback(() => setStillWaiting(false), []);

  return { waiting, stillWaiting, start, resetStill };
}
