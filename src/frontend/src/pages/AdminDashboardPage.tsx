import CreateGroupCard from "@/components/groups/CreateGroupCard";
import GroupsList from "@/components/groups/GroupsList";
import ToolsManagement from "@/components/tools/ToolsManagement";
import { cn } from "@/lib/utils";
import { MessageSquare, Wrench } from "lucide-react";
import { useState } from "react";

export default function AdminDashboardPage() {
  const [activeTab, setActiveTab] = useState<"groups" | "tools">("groups");

  return (
    <div
      data-ocid="dashboard.page"
      dir="rtl"
      className="mx-auto flex w-full max-w-6xl flex-col gap-6 px-4 py-6 md:px-6"
    >
      <div className="flex border-b border-border pb-2">
        <div className="flex gap-2">
          <button
            type="button"
            data-ocid="dashboard.tab_groups"
            onClick={() => setActiveTab("groups")}
            className={cn(
              "flex items-center gap-2 rounded-lg px-4 py-2 text-sm font-semibold transition-colors",
              activeTab === "groups"
                ? "bg-secondary text-foreground shadow-xs"
                : "text-muted-foreground hover:bg-secondary/50 hover:text-foreground",
            )}
          >
            <MessageSquare className="size-4" />
            المجموعات
          </button>
          <button
            type="button"
            data-ocid="dashboard.tab_tools"
            onClick={() => setActiveTab("tools")}
            className={cn(
              "flex items-center gap-2 rounded-lg px-4 py-2 text-sm font-semibold transition-colors",
              activeTab === "tools"
                ? "bg-secondary text-foreground shadow-xs"
                : "text-muted-foreground hover:bg-secondary/50 hover:text-foreground",
            )}
          >
            <Wrench className="size-4" />
            الأدوات (Tools)
          </button>
        </div>
      </div>

      {activeTab === "groups" ? (
        <div className="grid gap-6 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)] lg:items-start">
          <div className="order-2 lg:order-1">
            <CreateGroupCard />
          </div>
          <div className="order-1 lg:order-2">
            <GroupsList />
          </div>
        </div>
      ) : (
        <ToolsManagement />
      )}
    </div>
  );
}
