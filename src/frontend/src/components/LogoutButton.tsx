import { Button } from "@/components/ui/button";
import { authKey } from "@/hooks/useAdminQueries";
import { logout } from "@/lib/api";
import { useQueryClient } from "@tanstack/react-query";
import { LogOut } from "lucide-react";

export default function LogoutButton() {
  const queryClient = useQueryClient();
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
  return (
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
  );
}
