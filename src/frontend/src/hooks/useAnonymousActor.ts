import { createActor } from "@/backend";
import { createActorWithConfig } from "@caffeineai/core-infrastructure";
import { useQuery } from "@tanstack/react-query";

/**
 * Backend actor that is always anonymous, regardless of any stored Internet
 * Identity session. Used by the public agent room, which must never need login.
 */
export function useAnonymousActor() {
  const query = useQuery({
    queryKey: ["anonymous-actor"],
    queryFn: () => createActorWithConfig(createActor),
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: Number.POSITIVE_INFINITY,
  });
  return { actor: query.data ?? null, isFetching: query.isFetching };
}
