import { memo } from "react";
import { useTranslation } from "react-i18next";
import { Plus, LogOut } from "lucide-react";
import { Button } from "@/components/ui/button";
import { SessionSwitcher } from "@/components/chat/session-switcher";
import type { SessionInfo } from "@/types/session";
import { useAuthStore } from "@/stores/use-auth-store";
import { useNavigate } from "react-router";

interface ChatSidebarProps {
  agentId: string;
  onAgentChange: (agentId: string) => void;
  sessions: SessionInfo[];
  sessionsLoading: boolean;
  activeSessionKey: string;
  onSessionSelect: (key: string) => void;
  onDeleteSession?: (key: string) => void;
  onNewChat: () => void;
}

export const ChatSidebar = memo(function ChatSidebar({
  sessions,
  sessionsLoading,
  activeSessionKey,
  onSessionSelect,
  onDeleteSession,
  onNewChat,
}: ChatSidebarProps) {
  const { t } = useTranslation("chat");
  const logout = useAuthStore((s) => s.logout);
  const navigate = useNavigate();

  const handleLogout = () => {
    logout();
    navigate("/login");
  };

  return (
    <div className="flex h-full w-72 max-w-[85vw] flex-col border-r bg-background">
      {/* New chat button */}
      <div className="p-3">
        <Button
          variant="outline"
          className="w-full justify-start gap-2 border-primary/20 hover:bg-primary/5 hover:text-primary transition-all shadow-sm"
          onClick={onNewChat}
        >
          <Plus className="h-4 w-4" />
          {t("newChat")}
        </Button>
      </div>

      {/* Session list */}
      <div className="flex-1 overflow-y-auto">
        <SessionSwitcher
          sessions={sessions}
          activeKey={activeSessionKey}
          onSelect={onSessionSelect}
          onDelete={onDeleteSession}
          loading={sessionsLoading}
        />
      </div>

      {/* Footer Actions */}
      <div className="border-t p-3">
        <Button
          variant="ghost"
          className="w-full justify-start gap-2 text-muted-foreground hover:text-destructive hover:bg-destructive/5 transition-colors"
          onClick={handleLogout}
        >
          <LogOut className="h-4 w-4" />
          {t("common:logout", { defaultValue: "Logout" })}
        </Button>
      </div>
    </div>
  );
});
