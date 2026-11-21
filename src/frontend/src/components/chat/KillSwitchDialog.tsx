import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import type { GroupControls } from "./useGroupControls";

export function KillSwitchDialog({ controls }: { controls: GroupControls }) {
  return (
    <AlertDialog
      open={controls.killDialogOpen}
      onOpenChange={controls.setKillDialogOpen}
    >
      <AlertDialogContent
        dir="rtl"
        data-testid="kill-switch-dialog"
        data-ocid="groupchat.kill_dialog"
        className="border-border bg-card text-foreground sm:max-w-md"
      >
        <AlertDialogHeader className="text-right sm:text-right">
          <AlertDialogTitle
            data-testid="kill-switch-title"
            className="font-display text-lg font-bold text-destructive"
          >
            تأكيد الإيقاف الفوري
          </AlertDialogTitle>
          <AlertDialogDescription
            data-testid="kill-switch-description"
            className="text-sm leading-relaxed text-muted-foreground"
          >
            هل أنت متأكد من الإيقاف الفوري؟ سيتم إنهاء المحادثة ولا يمكن استئنافها.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter className="flex-row-reverse justify-start gap-2 sm:justify-start">
          <AlertDialogAction
            data-testid="kill-switch-confirm-button"
            data-ocid="groupchat.kill_confirm_button"
            onClick={controls.confirmKill}
            disabled={controls.busy}
            className="rounded-full bg-destructive text-destructive-foreground hover:bg-destructive/90"
          >
            تأكيد الإيقاف الفوري
          </AlertDialogAction>
          <AlertDialogCancel
            data-testid="kill-switch-cancel-button"
            data-ocid="groupchat.kill_cancel_button"
            onClick={controls.cancelKill}
            className="rounded-full border-border bg-secondary text-secondary-foreground hover:bg-accent"
          >
            إلغاء
          </AlertDialogCancel>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
