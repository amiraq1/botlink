import { cn } from "@/lib/utils";
import { AlertTriangle, CheckCircle2, Info, X } from "lucide-react";
import {
  type ReactNode,
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
} from "react";

type ToastTone = "success" | "danger" | "info";

interface ToastItem {
  id: number;
  title: string;
  description?: string;
  tone: ToastTone;
}

interface ToastApi {
  toast: (input: {
    title: string;
    description?: string;
    tone?: ToastTone;
  }) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

const TONE: Record<ToastTone, { icon: ReactNode; accent: string }> = {
  success: {
    icon: <CheckCircle2 aria-hidden="true" className="size-4" />,
    accent: "text-success",
  },
  danger: {
    icon: <AlertTriangle aria-hidden="true" className="size-4" />,
    accent: "text-destructive",
  },
  info: {
    icon: <Info aria-hidden="true" className="size-4" />,
    accent: "text-info",
  },
};

let nextId = 0;

/** Minimal toast host: bottom-start stack, auto-dismiss, announced politely. */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);

  const dismiss = useCallback((id: number) => {
    setItems((current) => current.filter((item) => item.id !== id));
  }, []);

  const toast = useCallback<ToastApi["toast"]>(
    ({ title, description, tone = "info" }) => {
      nextId += 1;
      const id = nextId;
      setItems((current) => [...current, { id, title, description, tone }]);
      window.setTimeout(() => dismiss(id), 4200);
    },
    [dismiss],
  );

  const value = useMemo(() => ({ toast }), [toast]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div
        aria-live="polite"
        aria-atomic="false"
        className="pointer-events-none fixed bottom-4 start-4 z-[80] flex w-[min(22rem,calc(100vw-2rem))] flex-col gap-2"
      >
        {items.map((item) => (
          <div
            key={item.id}
            data-slot="toast"
            className="pointer-events-auto flex animate-rise-in items-start gap-2.5 rounded-lg border border-border bg-elevated/95 p-3 shadow-lg backdrop-blur-md"
          >
            <span className={cn("mt-0.5 shrink-0", TONE[item.tone].accent)}>
              {TONE[item.tone].icon}
            </span>
            <div className="min-w-0 flex-1">
              <p className="text-xs font-bold text-foreground">{item.title}</p>
              {item.description ? (
                <p className="mt-0.5 text-2xs leading-relaxed text-muted-foreground">
                  {item.description}
                </p>
              ) : null}
            </div>
            <button
              type="button"
              aria-label="إغلاق التنبيه"
              onClick={() => dismiss(item.id)}
              className="shrink-0 rounded-full p-1 text-muted-foreground transition-colors duration-fast hover:bg-secondary hover:text-foreground"
            >
              <X aria-hidden="true" className="size-3.5" />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

/** Raises a toast from any component below ToastProvider. */
export function useToast(): ToastApi {
  const context = useContext(ToastContext);
  if (!context) throw new Error("useToast must be used inside ToastProvider");
  return context;
}
