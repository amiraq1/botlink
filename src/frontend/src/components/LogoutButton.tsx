import { Button } from "@/components/ui/button";
import { useInternetIdentity } from "@caffeineai/core-infrastructure";
import { LogOut } from "lucide-react";

export default function LogoutButton() {
  const { clear } = useInternetIdentity();
  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      data-ocid="auth.logout_button"
      className="rounded-full bg-secondary transition-smooth"
      onClick={() => clear()}
    >
      <LogOut aria-hidden="true" />
      تسجيل الخروج
    </Button>
  );
}
