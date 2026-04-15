import { Outlet, useLocation } from "react-router";
import { WifiOff } from "lucide-react";
import { useTranslation } from "react-i18next";
import { ErrorBoundary } from "@/components/shared/error-boundary";
import { useAuthStore } from "@/stores/use-auth-store";

/**
 * LiteLayout is the primary layout for light-web.
 * It focusing entirely on the chat interface with a premium aesthetic.
 */
export function LiteLayout() {
  const { t } = useTranslation("common");
  const location = useLocation();
  const connected = useAuthStore((s) => s.connected);

  function stableErrorBoundaryKey(pathname: string): string {
    const base = pathname.replace(/^(\/[^/]+)\/.*$/, "$1");
    return base;
  }

  return (
    <div className="flex h-dvh overflow-hidden bg-background relative font-sans">
      {/* 
        Premium Background Gradient:
        Deep Blue (#003D7A) to Cyan Green (#4DB8A8)
      */}
      <div 
        className="fixed inset-0 -z-10 opacity-[0.03] dark:opacity-[0.1]"
        style={{
          background: "linear-gradient(135deg, #003D7A 0%, #4DB8A8 100%)",
        }}
      />
      
      <div className="flex flex-1 flex-col overflow-hidden">
        {/* Gateway Disconnected Alert */}
        {!connected && (
          <div className="flex items-center gap-2 border-b border-destructive/30 bg-destructive/10 px-4 py-2 text-sm text-destructive animate-in fade-in slide-in-from-top-1 z-50">
            <WifiOff className="h-4 w-4 shrink-0" />
            <span>{t("disconnectedGateway")}</span>
          </div>
        )}

        <main className="min-w-0 flex-1 overflow-hidden">
          <ErrorBoundary key={stableErrorBoundaryKey(location.pathname)}>
            <div className="h-full w-full">
              <Outlet />
            </div>
          </ErrorBoundary>
        </main>
      </div>
    </div>
  );
}
