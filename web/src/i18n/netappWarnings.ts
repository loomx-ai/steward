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
