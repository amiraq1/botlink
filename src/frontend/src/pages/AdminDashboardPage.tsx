import CreateGroupCard from "@/components/groups/CreateGroupCard";
import GroupsList from "@/components/groups/GroupsList";
import ManagedAgentsManagement from "@/components/managed/ManagedAgentsManagement";
import ToolsManagement from "@/components/tools/ToolsManagement";
import { cn } from "@/lib/utils";
import { Bot, MessageSquare, Wrench } from "lucide-react";
import { useState } from "react";

const TABS = [
  { id: "groups", label: "المجموعات", icon: MessageSquare },
  { id: "tools", label: "الأدوات", icon: Wrench },
  { id: "managed", label: "الوكلاء المُدارون", icon: Bot },
] as const;

export default function AdminDashboardPage() {
  const [activeTab, setActiveTab] = useState<"groups" | "tools" | "managed">(
    "groups",
  );

  return (
    <div
      data-ocid="dashboard.page"
      dir="rtl"
      className="mx-auto flex w-full max-w-7xl flex-col gap-6 px-4 py-6 md:px-6 md:py-8"
    >
      <header className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="font-display text-2xl font-bold text-foreground">
            لوحة التحكم
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            أنشئ مجموعات البوتات وتابع أدوارها ورسائلها مباشرة.
          </p>
        </div>
        <div
          role="tablist"
          aria-label="أقسام لوحة التحكم"
          className="inline-flex w-fit items-center gap-1 rounded-full border border-border bg-surface/70 p-1"
        >
          {TABS.map((tab) => {
            const active = activeTab === tab.id;
            const Icon = tab.icon;
            return (
              <button
                key={tab.id}
                type="button"
                role="tab"
                aria-selected={active}
                data-ocid={`dashboard.tab_${tab.id}`}
                onClick={() => setActiveTab(tab.id)}
                className={cn(
                  "inline-flex h-9 items-center gap-2 rounded-full px-4 text-sm font-semibold transition-[background-color,color,box-shadow] duration-fast ease-out",
                  active
                    ? "bg-primary text-primary-foreground shadow-xs"
                    : "text-muted-foreground hover:bg-secondary hover:text-foreground",
                )}
              >
                <Icon aria-hidden="true" className="size-4" />
                {tab.label}
              </button>
            );
          })}
        </div>
      </header>

      {activeTab === "groups" ? (
        <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(0,26rem)] xl:items-start">
          <div className="min-w-0">
            <GroupsList />
          </div>
          <div className="min-w-0 xl:sticky xl:top-6">
            <CreateGroupCard />
          </div>
        </div>
      ) : activeTab === "tools" ? (
        <ToolsManagement />
      ) : (
        <ManagedAgentsManagement />
      )}
    </div>
  );
}
