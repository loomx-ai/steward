import type { Locale } from "./locales";

export const dataProtectionInstanceDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this backup instance stops its backups and requests deletion of its backup data under Azure retention rules. Soft-deleted data may remain recoverable and incur charges; completion does not mean permanent purge. The source workload, backup policy, vault and other backup instances are retained.",
  "zh-CN":
    "删除此备份实例将停止其备份，并按 Azure 保留规则请求删除其备份数据。软删除的数据可能仍可恢复并产生费用；任务完成不代表数据已永久清除。源工作负载、备份策略、保险库和其他备份实例将保留。",
};
