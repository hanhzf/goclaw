import { Suspense } from "react";
import { Routes, Route, Navigate } from "react-router";
import { LiteLayout } from "@/components/layout/lite-layout";
import { RequireAuth } from "@/components/shared/require-auth";
import { ErrorBoundary } from "@/components/shared/error-boundary";
import { ROUTES } from "@/lib/constants";
import { lazyWithRetry } from "@/lib/lazy-with-retry";

// Lazy-loaded pages (only chat and login)
const LoginPage = lazyWithRetry(() =>
  import("@/pages/login/login-page").then((m) => ({ default: m.LoginPage })),
);
const ChatPage = lazyWithRetry(() =>
  import("@/pages/chat/chat-page").then((m) => ({ default: m.ChatPage })),
);
const TenantSelectorPage = lazyWithRetry(() =>
  import("@/pages/login/tenant-selector").then((m) => ({ default: m.TenantSelectorPage })),
);

function PageLoader() {
  return (
    <div className="flex h-full items-center justify-center">
      <img src="/goclaw-icon.svg" alt="" className="h-8 w-8 animate-pulse opacity-50" />
    </div>
  );
}

export function AppRoutes() {
  return (
    <ErrorBoundary>
      <Suspense fallback={<PageLoader />}>
        <Routes>
          <Route path={ROUTES.LOGIN} element={<LoginPage />} />

          {/* Tenant selector — accessible when authenticated but tenant not yet selected */}
          <Route path={ROUTES.SELECT_TENANT} element={<TenantSelectorPage />} />

          {/* Main app — requires auth */}
          <Route
            element={
              <RequireAuth>
                {/* 
                  Removed RequireSetup for light-web to simplify experience.
                  Implicitly assumes backend is ready or handled.
                */}
                <LiteLayout />
              </RequireAuth>
            }
          >
            <Route index element={<Navigate to={ROUTES.CHAT} replace />} />
            <Route path={ROUTES.CHAT_PATTERN} element={<ChatPage />} />
            <Route path="*" element={<Navigate to={ROUTES.CHAT} replace />} />
          </Route>
        </Routes>
      </Suspense>
    </ErrorBoundary>
  );
}
