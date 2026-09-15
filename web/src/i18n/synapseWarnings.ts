import type { Locale } from "./locales";

export const synapseWorkspaceDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this workspace removes its SQL pools, compute engines, code artifacts and workspace metadata, and interrupts workspace workloads. Linked Data Lake storage is retained. SQL backups may remain recoverable under Azure retention; this operation does not purge them.",
  "zh-CN":
    "删除此工作区将移除 SQL 池、计算引擎、代码资产和工作区元数据，并中断工作区内的任务。关联的 Data Lake 存储将保留。SQL 备份可能仍在 Azure 保留期内可恢复，此操作不会清除这些备份。",
};

export const synapseSQLDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this SQL pool removes its database and interrupts queries and consumers. The workspace and other pools are retained. SQL backups may remain recoverable under Azure retention; this operation does not purge them.",
  "zh-CN":
    "删除此 SQL 池将移除其数据库，并中断查询及使用方访问。工作区和其他池将保留。SQL 备份可能仍在 Azure 保留期内可恢复，此操作不会清除这些备份。",
};

export const synapseRestorePointDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this user-defined restore point removes that recovery option. The SQL pool, workspace and other backups are retained.",
  "zh-CN":
    "删除此用户还原点将移除对应的恢复选项。SQL 池、工作区和其他备份将保留。",
};
