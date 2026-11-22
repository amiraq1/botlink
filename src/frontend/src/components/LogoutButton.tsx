import { Button } from "@/components/ui/button";
import { authKey, useRevokeAllSessions } from "@/hooks/useAdminQueries";
import { logout } from "@/lib/api";
import { useQueryClient } from "@tanstack/react-query";
import { LogOut, ShieldOff } from "lucide-react";
import { useState } from "react";

export default function LogoutButton() {
  const queryClient = useQueryClient();
  const revokeAll = useRevokeAllSessions();
  const [confirming, setConfirming] = useState(false);

  const handleLogout = async () => {
    try {
      await logout();
    } catch {
      // Even if the request fails, drop the local session state.
    }
    queryClient.clear();
    await queryClient.invalidateQueries({ queryKey: authKey });
    window.location.href = "/admin";
  };

  const handleRevokeAll = async () => {
    if (!confirming) {
      setConfirming(true);
      return;
    }
    try {
      await revokeAll.mutateAsync();
    } finally {
      await handleLogout();
    }
  };

  return (
    <div className="flex items-center gap-2">
      <Button
        type="button"
        variant="outline"
        size="sm"
        data-ocid="auth.logout_button"
        className="rounded-full bg-secondary transition-smooth"
        onClick={() => void handleLogout()}
      >
        <LogOut aria-hidden="true" />
        تسجيل الخروج
      </Button>
      <Button
        type="button"
        variant="outline"
        size="sm"
        data-ocid="auth.revoke_all_button"
        disabled={revokeAll.isPending}
        className={
          confirming
            ? "rounded-full border-destructive bg-destructive/10 text-destructive"
            : "rounded-full bg-secondary transition-smooth"
        }
        onClick={() => void handleRevokeAll()}
      >
        <ShieldOff aria-hidden="true" />
        {confirming ? "تأكيد إبطال كل الجلسات" : "إبطال كل الجلسات"}
      </Button>
    </div>
  );
}
