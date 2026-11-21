import CreateGroupCard from "@/components/groups/CreateGroupCard";
import GroupsList from "@/components/groups/GroupsList";

export default function AdminDashboardPage() {
  return (
    <div
      data-ocid="dashboard.page"
      dir="rtl"
      className="mx-auto grid w-full max-w-6xl gap-6 px-4 py-6 md:px-6 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)] lg:items-start"
    >
      <div className="order-2 lg:order-1">
        <CreateGroupCard />
      </div>
      <div className="order-1 lg:order-2">
        <GroupsList />
      </div>
    </div>
  );
}
