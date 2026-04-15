import { useNavigate, useLocation } from "react-router";
import { useAuthStore } from "@/stores/use-auth-store";
import { ROUTES } from "@/lib/constants";
import { LoginLayout } from "./login-layout";
import { TokenForm } from "./token-form";

export function LoginPage() {
  const setCredentials = useAuthStore((s) => s.setCredentials);
  const navigate = useNavigate();
  const location = useLocation();

  const from =
    (location.state as { from?: { pathname: string } })?.from?.pathname ??
    ROUTES.CHAT;

  function handleTokenLogin(userId: string, token: string) {
    setCredentials(token, userId);
    navigate(from, { replace: true });
  }

  return (
    <LoginLayout subtitle="GoClaw Chat Client">
      <TokenForm onSubmit={handleTokenLogin} />
    </LoginLayout>
  );
}
