package azure

const netappBackupPolicyType = netappAccountType + "/backupPolicies"

func netappPolicyKind(kind string) bool {
	return kind == netappSnapshotPolicyType || kind == netappBackupPolicyType
}

func netappPolicySnapshot(raw map[string]any, kind string) map[string]any {
	out := hybridComputeChildSnapshot(raw)
	if kind == netappBackupPolicyType {
		p := object(out["properties"])
		delete(p, "volumesAssigned")
		delete(p, "volumeBackups")
	}
	return out
}

func netappDetachedPolicyVolume(raw map[string]any, kind string) map[string]any {
	if kind != netappBackupPolicyType {
		return netappDetachedSnapshotVolume(raw)
	}
	out := hybridComputeChildSnapshot(raw)
	p := object(out["properties"])
	dp := object(p["dataProtection"])
	backup := object(dp["backup"])
	delete(backup, "backupPolicyId")
	delete(backup, "policyEnforced")
	if len(backup) == 0 {
		delete(dp, "backup")
	}
	if len(dp) == 0 {
		delete(p, "dataProtection")
	}
	return out
}

func netappBackupEnforced(raw map[string]any) any {
	return object(object(object(raw["properties"])["dataProtection"])["backup"])["policyEnforced"]
}

func (a *netappPolicyAction) protocol() string {
	if a.planned.Identity.NativeType == netappBackupPolicyType {
		return "netapp-backup-policy-delete-1"
	}
	return "netapp-snapshot-policy-delete-1"
}

func (a *netappPolicyAction) updateKey(field string) string {
	if field == "policyEnforced" {
		return ":pause:"
	}
	return ":detach:"
}

func (a *netappPolicyAction) updateComplete(raw map[string]any, pause bool) bool {
	if object(raw["properties"])["provisioningState"] != "Succeeded" {
		return false
	}
	if pause {
		return netappBackupEnforced(raw) == false
	}
	assignments, err := netappAssignments(raw)
	return err == nil && assignments[a.planned.Identity.NativeType] == "" && (a.planned.Identity.NativeType != netappBackupPolicyType || netappBackupEnforced(raw) == false)
}
