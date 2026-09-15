import type { Locale } from "./locales";

export const synapseWorkspaceDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this workspace permanently removes SQL pool data, compute engines, code artifacts and workspace metadata, and interrupts workspace workloads. Linked Data Lake storage is retained.",
  "zh-CN":
    "删除此工作区将永久移除 SQL 池数据、计算引擎、代码资产和工作区元数据，并中断工作区内的任务。关联的 Data Lake 存储将保留。",
};
