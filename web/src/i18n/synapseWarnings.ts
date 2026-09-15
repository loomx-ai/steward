import type { Locale } from "./locales";

export const synapseWorkspaceDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this workspace removes its SQL pools, compute engines, code artifacts and workspace metadata, and interrupts workspace workloads. Linked Data Lake storage is retained. SQL backups may remain recoverable under Azure retention; this operation does not purge them.",
  "zh-CN":
    "删除此工作区将移除 SQL 池、计算引擎、代码资产和工作区元数据，并中断工作区内的任务。关联的 Data Lake 存储将保留。SQL 备份可能仍在 Azure 保留期内可恢复，此操作不会清除这些备份。",
};
