import { useOptionalAuth } from "./AuthProvider";

export type Role = "viewer" | "operator" | "admin";

const rank: Record<Role, number> = { viewer: 0, operator: 1, admin: 2 };

// useHasRole hides controls the server would refuse. Until the principal is
// known it allows the control; the server still enforces every role.
export function useHasRole(role: Role) {
  const principal = useOptionalAuth()?.principal;
  if (!principal) return true;
  return principal.roles.some(
    (value) => value in rank && rank[value as Role] >= rank[role],
  );
}
