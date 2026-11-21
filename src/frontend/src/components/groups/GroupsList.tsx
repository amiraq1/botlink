import { useGroups } from "@/hooks/useAdminQueries";
import { AlertCircle, MessagesSquare, RefreshCw } from "lucide-react";
import GroupCard from "./GroupCard";

const SKELETON_IDS = [
  "group-skeleton-a",
  "group-skeleton-b",
  "group-skeleton-c",
];

export default function GroupsList() {
  const { data, isLoading, isError, refetch, isFetching } = useGroups();

  return (
    <section aria-labelledby="groups-list-title" className="space-y-3">
      <h2 id="groups-list-title" className="font-display text-xl font-bold">
        مجموعاتك
      </h2>

      {isLoading ? (
        <ul
          data-ocid="groups.loading_state"
          aria-busy="true"
          className="space-y-3"
        >
          {SKELETON_IDS.map((id) => (
            <li key={id} className="admin-card h-36 animate-pulse" />
          ))}
        </ul>
      ) : isError ? (
        <div
          role="alert"
          data-ocid="groups.error_state"
          className="admin-card flex flex-col items-center gap-3 p-6 text-center"
        >
          <AlertCircle aria-hidden="true" className="size-8 text-destructive" />
          <p className="text-sm">تعذر تحميل المجموعات.</p>
          <button
            type="button"
            data-ocid="groups.retry_button"
            onClick={() => void refetch()}
            disabled={isFetching}
            className="inline-flex h-10 items-center gap-2 rounded-full bg-secondary px-4 text-sm font-medium hover:bg-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-60"
          >
            <RefreshCw aria-hidden="true" className="size-4" />
            إعادة المحاولة
          </button>
        </div>
      ) : !data || data.length === 0 ? (
        <div
          data-ocid="groups.empty_state"
          className="admin-card flex flex-col items-center gap-2 p-8 text-center"
        >
          <span className="flex size-14 items-center justify-center rounded-full bg-primary/15 text-primary">
            <MessagesSquare aria-hidden="true" className="size-7" />
          </span>
          <p className="font-display text-base font-semibold">
            لا توجد مجموعات بعد
          </p>
          <p className="text-sm text-muted-foreground">
            أنشئ مجموعتك الأولى من النموذج لتبدأ محادثة البوتات.
          </p>
        </div>
      ) : (
        <ul data-ocid="groups.list" className="space-y-3">
          {data.map((group, index) => (
            <GroupCard
              key={group.id.toString()}
              group={group}
              position={index + 1}
            />
          ))}
        </ul>
      )}
    </section>
  );
}
