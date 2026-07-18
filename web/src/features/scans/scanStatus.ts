export function scanStatusLabel(
  status: string,
  defaultLabel: string,
  inProgressLabel: string,
) {
  return status === "reconciling" ? inProgressLabel : defaultLabel;
}
