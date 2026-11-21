import { GroupStatus } from "@/backend";
import {
  usePauseGroup,
  useResumeGroup,
  useStopGroup,
} from "@/hooks/useAdminQueries";
import { isGroupEnded } from "@/lib/convert";
import { useState } from "react";

/** Pause / resume / kill-switch actions shared by the top bar and the info drawer. */
export function useGroupControls(groupId: bigint, status: GroupStatus) {
  const pause = usePauseGroup();
  const resume = useResumeGroup();
  const stop = useStopGroup();
  const [killDialogOpen, setKillDialogOpen] = useState(false);

  const ended = isGroupEnded(status);
  const canPause =
    status === GroupStatus.running || status === GroupStatus.waiting_for_leader;
  const canResume = status === GroupStatus.paused;

  const requestKill = () => {
    if (ended) return;
    setKillDialogOpen(true);
  };

  const confirmKill = () => {
    setKillDialogOpen(false);
    stop.mutate(groupId);
  };

  const cancelKill = () => {
    setKillDialogOpen(false);
  };

  const error = pause.error ?? resume.error ?? stop.error;

  return {
    ended,
    canPause,
    canResume,
    busy: pause.isPending || resume.isPending || stop.isPending,
    pause: () => pause.mutate(groupId),
    resume: () => resume.mutate(groupId),
    killSwitch: requestKill,
    killDialogOpen,
    setKillDialogOpen,
    confirmKill,
    cancelKill,
    error: error ? "تعذر تنفيذ الإجراء. حاول مرة أخرى." : null,
  };
}

export type GroupControls = ReturnType<typeof useGroupControls>;
