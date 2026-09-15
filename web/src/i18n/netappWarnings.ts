import type { Locale } from "./locales";

export const netappVolumeDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this volume removes its data, snapshots, subvolumes and quota rules. Stop applications and unmount the volume from all hosts before proceeding. Backups in backup vaults, the capacity pool and the NetApp account are retained.",
  "zh-CN":
    "删除此卷将移除卷内数据、快照、子卷和配额规则。继续前，请停止应用并从所有主机卸载此卷。备份保管库内的备份、容量池和 NetApp 帐户将保留。",
};
