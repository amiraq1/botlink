import type { GroupId } from "@/lib/api";
import { groupKey } from "@/lib/queryKeys";
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";

/** Coalesce bursts (mock reply + turn advance) but keep the first pulse fast. */
const PULSE_MIN_GAP_MS = 100;

function useEventSource(target: string | null, onPulse: () => void): boolean {
  const [connected, setConnected] = useState(false);
  const pulseRef = useRef(onPulse);
  pulseRef.current = onPulse;

  useEffect(() => {
    if (target === null || typeof EventSource === "undefined") {
      setConnected(false);
      return;
    }
    let source: EventSource | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let lastFire = 0;
    let disposed = false;

    const fire = () => {
      timer = null;
      lastFire = Date.now();
      pulseRef.current();
    };
    const schedule = () => {
      if (timer !== null) return;
      const wait = Math.max(0, PULSE_MIN_GAP_MS - (Date.now() - lastFire));
      timer = setTimeout(fire, wait);
    };
    const open = () => {
      if (disposed || source !== null || document.hidden) return;
      source = new EventSource(target);
      source.onopen = () => setConnected(true);
      // EventSource reconnects on its own; polling stays the fallback until
      // the next successful open.
      source.onerror = () => setConnected(false);
      source.addEventListener("change", schedule);
    };
    const close = () => {
      if (source !== null) {
        source.close();
        source = null;
      }
      setConnected(false);
    };
    const onVisibility = () => {
      if (document.hidden) close();
      else open();
    };

    open();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      disposed = true;
      document.removeEventListener("visibilitychange", onVisibility);
      if (timer !== null) clearTimeout(timer);
      close();
    };
  }, [target]);

  return connected;
}

/**
 * Live admin-chat events for one group. Returns whether the stream is open so
 * the detail query can slow its fallback polling while SSE is healthy.
 */
export function useGroupEvents(groupId: GroupId | null): boolean {
  const queryClient = useQueryClient();
  const onPulse = useCallback(() => {
    if (groupId === null) return;
    void queryClient.invalidateQueries({ queryKey: groupKey(groupId) });
  }, [groupId, queryClient]);
  return useEventSource(
    groupId === null ? null : `/api/admin/groups/${groupId}/events`,
    onPulse,
  );
}

/** Live pulse for the public agent room; the page refetches its state. */
export function useRoomEvents(token: string, onPulse: () => void): boolean {
  return useEventSource(
    token.length > 0 ? `/api/room/${encodeURIComponent(token)}/events` : null,
    onPulse,
  );
}
