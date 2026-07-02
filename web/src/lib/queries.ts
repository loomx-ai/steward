import {
  useQuery,
  useMutation,
  useQueryClient,
  keepPreviousData,
} from "@tanstack/react-query";
import * as api from "./api";
import { useActor } from "./actor";

export const keys = {
  scans: ["scans"] as const,
  resources: (scanId: string, type: string, query: string) =>
    ["resources", scanId, type, query] as const,
  graph: (scanId: string) => ["graph", scanId] as const,
  candidates: (scanId: string) => ["candidates", scanId] as const,
  plans: ["plans"] as const,
  plan: (id: string) => ["plan", id] as const,
  audits: ["audits"] as const,
  savings: ["savings"] as const,
};

const anyRunning = (scans: api.ScanJob[]) =>
  scans.some((s) => s.status === "running" || s.status === "pending");

export function useScans() {
  return useQuery({
    queryKey: keys.scans,
    queryFn: api.listScans,
    refetchInterval: (q) =>
      q.state.data && anyRunning(q.state.data) ? 4000 : false,
  });
}

export function useResources(scanId: string, type: string, query: string) {
  return useQuery({
    queryKey: keys.resources(scanId, type, query),
    queryFn: () => api.listResources({ scanId, type, query }),
    placeholderData: keepPreviousData,
    enabled: scanId !== "",
  });
}

export function useGraph(scanId: string) {
  return useQuery({
    queryKey: keys.graph(scanId),
    queryFn: () => api.listGraph(scanId || undefined),
    placeholderData: keepPreviousData,
  });
}

export function useCandidates(scanId: string) {
  return useQuery({
    queryKey: keys.candidates(scanId),
    queryFn: () => api.listCandidates(scanId || undefined),
    placeholderData: keepPreviousData,
  });
}

export function usePlans() {
  return useQuery({
    queryKey: keys.plans,
    queryFn: api.listPlans,
    refetchInterval: (q) =>
      q.state.data?.some((p) => p.status === "running") ? 3000 : false,
  });
}

export function usePlan(id: string | undefined) {
  return useQuery({
    queryKey: keys.plan(id ?? ""),
    queryFn: () => api.getPlan(id as string),
    enabled: !!id,
    refetchInterval: (q) =>
      q.state.data?.plan.status === "running" ? 3000 : false,
  });
}

export function useAudits() {
  return useQuery({ queryKey: keys.audits, queryFn: api.listAudits });
}

export function useSavings() {
  return useQuery({ queryKey: keys.savings, queryFn: api.getSavingsReport });
}

export function useReconcile() {
  const qc = useQueryClient();
  const { actor } = useActor();
  return useMutation({
    mutationFn: (scanId: string) => api.reconcileScan(scanId, actor),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["graph"] });
      qc.invalidateQueries({ queryKey: ["candidates"] });
      qc.invalidateQueries({ queryKey: keys.savings });
    },
  });
}

export function useCreateScan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.createScan,
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.scans }),
  });
}

export function useUpdateCandidate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      status,
    }: {
      id: string;
      status: api.CleanupCandidate["status"];
    }) => api.updateCandidateStatus(id, status),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["candidates"] });
      qc.invalidateQueries({ queryKey: keys.savings });
    },
  });
}

export function useCreatePlan() {
  const qc = useQueryClient();
  const { actor } = useActor();
  return useMutation({
    mutationFn: ({
      ids,
      limits,
    }: {
      ids: string[];
      limits: api.PlanLimitsInput;
    }) => api.createPlan(ids, actor, limits),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.plans });
      qc.invalidateQueries({ queryKey: ["candidates"] });
    },
  });
}

export function useApprovePlan() {
  const qc = useQueryClient();
  const { actor } = useActor();
  return useMutation({
    mutationFn: (id: string) => api.approvePlan(id, actor),
    onSuccess: (_d, id) => {
      qc.invalidateQueries({ queryKey: keys.plans });
      qc.invalidateQueries({ queryKey: keys.plan(id) });
      qc.invalidateQueries({ queryKey: keys.audits });
    },
  });
}

export function useExecutePlan() {
  const qc = useQueryClient();
  const { actor } = useActor();
  return useMutation({
    mutationFn: (id: string) => api.executePlan(id, actor),
    onSuccess: (_d, id) => {
      qc.invalidateQueries({ queryKey: keys.plans });
      qc.invalidateQueries({ queryKey: keys.plan(id) });
      qc.invalidateQueries({ queryKey: keys.audits });
      qc.invalidateQueries({ queryKey: keys.savings });
    },
  });
}
