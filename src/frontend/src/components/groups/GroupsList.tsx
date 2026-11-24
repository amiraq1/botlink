import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton } from "@/components/ui/skeleton";
import { useGroups } from "@/hooks/useAdminQueries";
import { formatNumberAr } from "@/lib/convert";
import { AlertCircle, MessagesSquare, Plus, RefreshCw } from "lucide-react";
import GroupCard from "./GroupCard";

const SKELETON_IDS = [
  "group-skeleton-a",
  "group-skeleton-b",
  "group-skeleton-c",
  "group-skeleton-d",
];

export default function GroupsList() {
  const { data, isLoading, isError, refetch, isFetching } = useGroups();

  return (
    <section
      aria-labelledby="groups-list-title"
      className="flex flex-col gap-4"
    >
      <div className="flex items-end justify-between gap-3">
        <div>
          <h2
            id="groups-list-title"
            className="font-display text-xl font-bold text-foreground"
          >
            مجموعاتك
          </h2>
          <p className="mt-0.5 text-sm text-muted-foreground">
            {data && data.length > 0
              ? `${formatNumberAr(data.length)} مجموعة قيد التشغيل والمتابعة`
              : "كل محادثات البوتات في مكان واحد"}
          </p>
        </div>
        <Button
          asChild
          variant="secondary"
          size="sm"
          className="shrink-0"
          data-ocid="groups.create_button"
        >
          <a href="#create-group">
            <Plus aria-hidden="true" className="size-3.5" />
            إنشاء مجموعة
          </a>
        </Button>
      </div>

      {isLoading ? (
        <ul
          data-ocid="groups.loading_state"
          aria-busy="true"
          aria-label="جارٍ تحميل المجموعات"
          className="grid gap-4 sm:grid-cols-2"
        >
          {SKELETON_IDS.map((id) => (
            <li key={id} className="admin-card space-y-3 p-4">
              <div className="flex items-start justify-between gap-3">
                <div className="flex-1 space-y-2">
                  <Skeleton className="h-4 w-2/3" />
                  <Skeleton className="h-3 w-1/2" />
                </div>
                <Skeleton className="h-5 w-16 rounded-full" />
              </div>
              <Skeleton className="h-1.5 w-full rounded-full" />
              <Skeleton className="h-3 w-3/4" />
              <Skeleton className="h-9 w-28 rounded-full" />
            </li>
          ))}
        </ul>
      ) : isError ? (
        <EmptyState
          ocid="groups.error_state"
          icon={<AlertCircle />}
          title="تعذر تحميل المجموعات"
          description="لم يستجب الخادم. تحقق من الاتصال ثم أعد المحاولة."
          action={
            <Button
              variant="secondary"
              data-ocid="groups.retry_button"
              onClick={() => void refetch()}
              loading={isFetching}
            >
              <RefreshCw aria-hidden="true" className="size-4" />
              إعادة المحاولة
            </Button>
          }
        />
      ) : !data || data.length === 0 ? (
        <EmptyState
          ocid="groups.empty_state"
          icon={<MessagesSquare />}
          title="لا توجد مجموعات بعد"
          description="أنشئ مجموعتك الأولى، أضف من ٢ إلى ١٠ بوتات، وابدأ أول محادثة جماعية."
          action={
            <Button asChild>
              <a href="#create-group" data-ocid="groups.empty_create_button">
                <Plus aria-hidden="true" className="size-4" />
                إنشاء مجموعة
              </a>
            </Button>
          }
        />
      ) : (
        <ul
          data-ocid="groups.list"
          className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-2"
        >
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
