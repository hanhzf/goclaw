import i18n from "i18next";
import { initReactI18next } from "react-i18next";

// --- EN namespaces ---
import enCommon from "./locales/en/common.json";
import enLogin from "./locales/en/login.json";
import enChat from "./locales/en/chat.json";
import enSessions from "./locales/en/sessions.json";
import enAgents from "./locales/en/agents.json";

// --- VI namespaces ---
import viCommon from "./locales/vi/common.json";
import viLogin from "./locales/vi/login.json";
import viChat from "./locales/vi/chat.json";
import viSessions from "./locales/vi/sessions.json";
import viAgents from "./locales/vi/agents.json";

// --- ZH namespaces ---
import zhCommon from "./locales/zh/common.json";
import zhLogin from "./locales/zh/login.json";
import zhChat from "./locales/zh/chat.json";
import zhSessions from "./locales/zh/sessions.json";
import zhAgents from "./locales/zh/agents.json";

const STORAGE_KEY = "goclaw:language";

function getInitialLanguage(): string {
  const stored = localStorage.getItem(STORAGE_KEY);
  if (stored === "en" || stored === "vi" || stored === "zh") return stored;
  const lang = navigator.language.toLowerCase();
  if (lang.startsWith("vi")) return "vi";
  if (lang.startsWith("zh")) return "zh";
  return "en";
}

const ns = ["common", "login", "chat", "sessions", "agents"] as const;

i18n.use(initReactI18next).init({
  resources: {
    en: {
      common: enCommon,
      login: enLogin,
      chat: enChat,
      sessions: enSessions,
      agents: enAgents,
    },
    vi: {
      common: viCommon,
      login: viLogin,
      chat: viChat,
      sessions: viSessions,
      agents: viAgents,
    },
    zh: {
      common: zhCommon,
      login: zhLogin,
      chat: zhChat,
      sessions: zhSessions,
      agents: zhAgents,
    },
  },
  ns: [...ns],
  defaultNS: "common",
  lng: getInitialLanguage(),
  fallbackLng: "en",
  interpolation: { escapeValue: false },
  missingKeyHandler: import.meta.env.DEV
    ? (_lngs, _ns, key) => console.warn(`[i18n] missing: ${key}`)
    : undefined,
});

i18n.on("languageChanged", (lng) => {
  localStorage.setItem(STORAGE_KEY, lng);
  document.documentElement.lang = lng;
});

export default i18n;
