import type { Locale } from "./locales";

export const netappVolumeDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this volume removes its data, snapshots, subvolumes and quota rules. Stop applications and unmount the volume from all hosts before proceeding. Backups in backup vaults, the capacity pool and the NetApp account are retained.",
  "zh-CN":
    "删除此卷将移除卷内数据、快照、子卷和配额规则。继续前，请停止应用并从所有主机卸载此卷。备份保管库内的备份、容量池和 NetApp 帐户将保留。",
};

export const netappSnapshotDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this snapshot permanently removes that recovery point. The volume, other snapshots and backup-vault backups are retained.",
  "zh-CN":
    "删除此快照将永久移除对应的恢复点。卷、其他快照及备份保管库内的备份将保留。",
};

export const netappBackupDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this backup permanently removes that recovery point. The source volume and other backups are retained. Deleting the final backup also removes the reference point for future incremental backups.",
  "zh-CN":
    "删除此备份将永久移除对应的恢复点。源卷和其他备份将保留。删除最后一个备份还会移除后续增量备份的参考点。",
};

export const netappSubvolumeDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this subvolume removes its data and can interrupt applications using it. The parent volume, other subvolumes, snapshots and backup-vault backups are retained.",
  "zh-CN":
    "删除此子卷将移除其数据，并可能中断使用它的应用。父卷、其他子卷、快照及备份保管库内的备份将保留。",
};

export const netappQuotaDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this quota rule changes the storage limit for its users or groups. Other applicable quota rules can still apply. Files and the parent volume are retained.",
  "zh-CN":
    "删除此配额规则将改变相关用户或组的存储限制，其他适用的配额规则仍可能生效。文件和父卷将保留。",
};

export const netappPoolDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this capacity pool first deletes its reviewed volumes and their data, snapshots, subvolumes and quota rules. Stop applications and unmount these volumes before proceeding. The NetApp account and backup-vault backups are retained.",
  "zh-CN":
    "删除此容量池前，将先删除已审查的卷及其数据、快照、子卷和配额规则。继续前，请停止应用并卸载这些卷。NetApp 帐户和备份保管库内的备份将保留。",
};

export const netappSnapshotPolicyDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this snapshot policy first removes its assignment from the reviewed volumes, stopping future snapshots scheduled by this policy. The volumes, their existing snapshots, subvolumes, quota rules and backup-vault backups are retained.",
  "zh-CN":
    "删除此快照策略前，将先解除它与已审查卷的绑定，并停止由此策略安排的后续快照。卷、已有快照、子卷、配额规则和备份保管库内的备份将保留。",
};

export const netappBackupPolicyDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this backup policy first suspends scheduled backups and removes its assignment from the reviewed volumes. The volumes, their snapshots, subvolumes, quota rules, backup vaults and existing backups are retained.",
  "zh-CN":
    "删除此备份策略前，将先暂停计划备份并解除它与已审查卷的绑定。卷、快照、子卷、配额规则、备份保管库和已有备份将保留。",
};
