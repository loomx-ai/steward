import type { Locale } from "./locales";

export const dataProtectionInstanceDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this backup instance stops its backups and requests deletion of its backup data under Azure retention rules. Soft-deleted data may remain recoverable and incur charges; completion does not mean permanent purge. The source workload, backup policy, vault and other backup instances are retained.",
  "zh-CN":
    "删除此备份实例将停止其备份，并按 Azure 保留规则请求删除其备份数据。软删除的数据可能仍可恢复并产生费用；任务完成不代表数据已永久清除。源工作负载、备份策略、保险库和其他备份实例将保留。",
};

export const dataProtectionVaultDeletionWarning: Record<Locale, string> = {
  "en-US":
    "Deleting this backup vault removes the active vault after its active backup instances and policies have been removed. Azure may retain the vault and backup data under soft-delete rules, with recovery options and possible charges. Completion does not mean permanent purge. Source workloads and external Resource Guard resources are retained; security settings are not disabled.",
  "zh-CN":
    "删除此备份保险库前，必须先移除其活动备份实例和策略。Azure 可能按软删除规则保留保险库及备份数据，保留期间可能仍可恢复并产生费用。任务完成不代表数据已永久清除。源工作负载和外部 Resource Guard 资源将保留；不会禁用安全设置。",
};
