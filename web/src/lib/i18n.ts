export type Locale = "en-US" | "zh-CN";

type LabeledValue = {
  label: string;
  value: string;
};

export type Copy = {
  locale: Locale;
  htmlLang: string;
  subtitle: string;
  actions: {
    refresh: string;
    analyze: string;
    startScan: string;
    createPlan: string;
    accept: string;
    ignore: string;
    snooze: string;
    viewDetails: string;
    viewPlan: string;
    approvePlan: string;
    executePlan: string;
    exportMarkdown: string;
    exportJSON: string;
    exportAuditCSV: string;
    exportAuditJSON: string;
  };
  sections: {
    newScan: string;
    scanJobs: string;
    resources: string;
    resourceGraph: string;
    cleanupCandidates: string;
    cleanupPlans: string;
    planDetail: string;
    resourceDetail: string;
    savingsReport: string;
    auditLog: string;
  };
  fields: {
    account: string;
    regions: string;
    accessKeyID: string;
    accessKeySecret: string;
    maxResources: string;
    maxRegions: string;
    highRiskCap: string;
    noLimit: string;
    latestScan: string;
    allTypes: string;
    allStatuses: string;
    searchPlaceholder: string;
    approval: string;
    items: string;
    tags: string;
    raw: string;
    protected: string;
    nativeID: string;
    yes: string;
    no: string;
    unknown: string;
  };
  metrics: {
    resources: string;
    protected: string;
    activeJobs: string;
    openCandidates: string;
    monthlySavings: string;
  };
  table: {
    account: string;
    mode: string;
    regions: string;
    status: string;
    resources: string;
    created: string;
    name: string;
    type: string;
    region: string;
    state: string;
    team: string;
    source: string;
    relation: string;
    target: string;
    resource: string;
    rule: string;
    action: string;
    savings: string;
    plan: string;
    risk: string;
    time: string;
    actor: string;
    result: string;
    message: string;
  };
  empty: {
    noScans: string;
    noResources: string;
    runAnalyze: string;
    noCandidates: string;
    noPlans: string;
    noPlanSelected: string;
    noResourceSelected: string;
    noData: string;
    noAudits: string;
  };
  filters: {
    candidateStatuses: LabeledValue[];
  };
  labels: {
    statuses: Record<string, string>;
    risks: Record<string, string>;
    actions: Record<string, string>;
    resourceTypes: Record<string, string>;
    modes: Record<string, string>;
    dryRun: string;
    live: string;
    events: string;
    edges: string;
    plans: string;
    candidates: string;
    monthlyEstimate: string;
    completedPlans: string;
    byType: string;
    byTeam: string;
    perMonth: string;
    blocked: string;
    pending: string;
  };
  messages: {
    requestFailed: string;
    scanFailed: string;
    scanQueued: (id: string) => string;
    noScanSelected: string;
    analysisCompleted: (edges: number, candidates: number) => string;
    analysisFailed: string;
    resourceLoadFailed: string;
    selectOpenCandidate: string;
    planCreated: (id: string) => string;
    planCreationFailed: string;
    planLoadFailed: string;
    planApproved: (id: string) => string;
    planApprovalFailed: string;
    planExecuted: (id: string) => string;
    planExecutionFailed: string;
    candidateUpdateFailed: string;
    selectCandidateAria: (name: string) => string;
  };
};

export const copies: Record<Locale, Copy> = {
  "en-US": {
    locale: "en-US",
    htmlLang: "en",
    subtitle: "Local MVP Console",
    actions: {
      refresh: "Refresh",
      analyze: "Analyze",
      startScan: "Start Scan",
      createPlan: "Create Plan",
      accept: "Accept",
      ignore: "Ignore",
      snooze: "Snooze",
      viewDetails: "View details",
      viewPlan: "View plan",
      approvePlan: "Approve plan",
      executePlan: "Execute plan",
      exportMarkdown: "Export Markdown",
      exportJSON: "Export JSON",
      exportAuditCSV: "Export audit CSV",
      exportAuditJSON: "Export audit JSON",
    },
    sections: {
      newScan: "New Scan",
      scanJobs: "Scan Jobs",
      resources: "Resources",
      resourceGraph: "Resource Graph",
      cleanupCandidates: "Cleanup Candidates",
      cleanupPlans: "Cleanup Plans",
      planDetail: "Plan Detail",
      resourceDetail: "Resource Detail",
      savingsReport: "Savings Report",
      auditLog: "Audit Log",
    },
    fields: {
      account: "Account",
      regions: "Regions",
      accessKeyID: "AccessKey ID",
      accessKeySecret: "AccessKey Secret",
      maxResources: "Max resources",
      maxRegions: "Max regions",
      highRiskCap: "High risk cap",
      noLimit: "no limit",
      latestScan: "Latest scan",
      allTypes: "all types",
      allStatuses: "all statuses",
      searchPlaceholder: "Search resources",
      approval: "Approval",
      items: "Items",
      tags: "Tags",
      raw: "Raw",
      protected: "Protected",
      nativeID: "Native ID",
      yes: "yes",
      no: "no",
      unknown: "unknown",
    },
    metrics: {
      resources: "Resources",
      protected: "Protected",
      activeJobs: "Active Jobs",
      openCandidates: "Open Candidates",
      monthlySavings: "Monthly Savings",
    },
    table: {
      account: "Account",
      mode: "Mode",
      regions: "Regions",
      status: "Status",
      resources: "Resources",
      created: "Created",
      name: "Name",
      type: "Type",
      region: "Region",
      state: "State",
      team: "Team",
      source: "Source",
      relation: "Relation",
      target: "Target",
      resource: "Resource",
      rule: "Rule",
      action: "Action",
      savings: "Savings",
      plan: "Plan",
      risk: "Risk",
      time: "Time",
      actor: "Actor",
      result: "Result",
      message: "Message",
    },
    empty: {
      noScans: "No scans yet",
      noResources: "No resources for this filter",
      runAnalyze: "Run Analyze after a scan",
      noCandidates: "No candidates for this status",
      noPlans: "No cleanup plans yet",
      noPlanSelected: "No plan selected",
      noResourceSelected: "No resource selected",
      noData: "No data",
      noAudits: "No audit events yet",
    },
    filters: {
      candidateStatuses: [
        { label: "open", value: "open" },
        { label: "accepted", value: "accepted" },
        { label: "ignored", value: "ignored" },
        { label: "snoozed", value: "snoozed" },
        { label: "all statuses", value: "all" },
      ],
    },
    labels: {
      statuses: {
        pending: "pending",
        running: "running",
        succeeded: "succeeded",
        failed: "failed",
        open: "open",
        accepted: "accepted",
        ignored: "ignored",
        snoozed: "snoozed",
        draft: "draft",
        pending_approval: "pending approval",
        approved: "approved",
        completed: "completed",
        canceled: "canceled",
        blocked: "blocked",
      },
      risks: { low: "low", medium: "medium", high: "high" },
      actions: {
        tag: "tag",
        delete: "delete",
        release: "release",
        stop: "stop",
        detach: "detach",
      },
      resourceTypes: {
        ecs_instance: "ecs instance",
        disk: "disk",
        eip: "eip",
        security_group: "security group",
        snapshot: "snapshot",
        vpc: "vpc",
        vswitch: "vswitch",
      },
      modes: { demo: "demo", alicloud: "alicloud" },
      dryRun: "dry-run",
      live: "live",
      events: "events",
      edges: "edges",
      plans: "plans",
      candidates: "candidates",
      monthlyEstimate: "Monthly Estimate",
      completedPlans: "Completed Plans",
      byType: "By Type",
      byTeam: "By Team",
      perMonth: "/ mo",
      blocked: "blocked",
      pending: "pending",
    },
    messages: {
      requestFailed: "request failed",
      scanFailed: "scan failed",
      scanQueued: (id) => `scan ${id} queued`,
      noScanSelected: "no scan selected",
      analysisCompleted: (edges, candidates) =>
        `analysis completed: ${edges} edges, ${candidates} candidates`,
      analysisFailed: "analysis failed",
      resourceLoadFailed: "resource load failed",
      selectOpenCandidate: "select at least one open candidate",
      planCreated: (id) => `dry-run plan ${id} created`,
      planCreationFailed: "plan creation failed",
      planLoadFailed: "plan load failed",
      planApproved: (id) => `plan ${id} approved`,
      planApprovalFailed: "plan approval failed",
      planExecuted: (id) => `plan ${id} executed`,
      planExecutionFailed: "plan execution failed",
      candidateUpdateFailed: "candidate update failed",
      selectCandidateAria: (name) => `select ${name}`,
    },
  },
  "zh-CN": {
    locale: "zh-CN",
    htmlLang: "zh-CN",
    subtitle: "本地 MVP 控制台",
    actions: {
      refresh: "刷新",
      analyze: "分析",
      startScan: "开始扫描",
      createPlan: "创建计划",
      accept: "接受",
      ignore: "忽略",
      snooze: "稍后处理",
      viewDetails: "查看详情",
      viewPlan: "查看计划",
      approvePlan: "审批计划",
      executePlan: "执行计划",
      exportMarkdown: "导出 Markdown",
      exportJSON: "导出 JSON",
      exportAuditCSV: "导出审计 CSV",
      exportAuditJSON: "导出审计 JSON",
    },
    sections: {
      newScan: "新建扫描",
      scanJobs: "扫描任务",
      resources: "资源",
      resourceGraph: "资源拓扑",
      cleanupCandidates: "清理候选",
      cleanupPlans: "清理计划",
      planDetail: "计划详情",
      resourceDetail: "资源详情",
      savingsReport: "节省报告",
      auditLog: "审计日志",
    },
    fields: {
      account: "账号",
      regions: "地域",
      accessKeyID: "AccessKey ID",
      accessKeySecret: "AccessKey Secret",
      maxResources: "资源数上限",
      maxRegions: "地域数上限",
      highRiskCap: "高风险上限",
      noLimit: "不限",
      latestScan: "最新扫描",
      allTypes: "全部类型",
      allStatuses: "全部状态",
      searchPlaceholder: "搜索资源",
      approval: "审批",
      items: "条目",
      tags: "标签",
      raw: "原始数据",
      protected: "受保护",
      nativeID: "云资源 ID",
      yes: "是",
      no: "否",
      unknown: "未知",
    },
    metrics: {
      resources: "资源数",
      protected: "受保护",
      activeJobs: "活跃任务",
      openCandidates: "待处理候选",
      monthlySavings: "月度节省",
    },
    table: {
      account: "账号",
      mode: "模式",
      regions: "地域",
      status: "状态",
      resources: "资源数",
      created: "创建时间",
      name: "名称",
      type: "类型",
      region: "地域",
      state: "状态",
      team: "团队",
      source: "源资源",
      relation: "关系",
      target: "目标资源",
      resource: "资源",
      rule: "规则",
      action: "动作",
      savings: "节省",
      plan: "计划",
      risk: "风险",
      time: "时间",
      actor: "操作人",
      result: "结果",
      message: "消息",
    },
    empty: {
      noScans: "暂无扫描",
      noResources: "当前筛选下暂无资源",
      runAnalyze: "扫描后点击分析生成拓扑",
      noCandidates: "当前状态下暂无候选",
      noPlans: "暂无清理计划",
      noPlanSelected: "未选择计划",
      noResourceSelected: "未选择资源",
      noData: "暂无数据",
      noAudits: "暂无审计事件",
    },
    filters: {
      candidateStatuses: [
        { label: "待处理", value: "open" },
        { label: "已接受", value: "accepted" },
        { label: "已忽略", value: "ignored" },
        { label: "稍后处理", value: "snoozed" },
        { label: "全部状态", value: "all" },
      ],
    },
    labels: {
      statuses: {
        pending: "等待中",
        running: "运行中",
        succeeded: "成功",
        failed: "失败",
        open: "待处理",
        accepted: "已接受",
        ignored: "已忽略",
        snoozed: "稍后处理",
        draft: "草稿",
        pending_approval: "待审批",
        approved: "已审批",
        completed: "已完成",
        canceled: "已取消",
        blocked: "已阻塞",
      },
      risks: { low: "低", medium: "中", high: "高" },
      actions: {
        tag: "打标",
        delete: "删除",
        release: "释放",
        stop: "停止",
        detach: "解绑",
      },
      resourceTypes: {
        ecs_instance: "ECS 实例",
        disk: "云盘",
        eip: "EIP",
        security_group: "安全组",
        snapshot: "快照",
        vpc: "VPC",
        vswitch: "交换机",
      },
      modes: { demo: "演示", alicloud: "阿里云" },
      dryRun: "演练",
      live: "实际执行",
      events: "条事件",
      edges: "条关系",
      plans: "个计划",
      candidates: "个候选",
      monthlyEstimate: "月度预估",
      completedPlans: "已完成计划",
      byType: "按类型",
      byTeam: "按团队",
      perMonth: "/ 月",
      blocked: "阻塞",
      pending: "待执行",
    },
    messages: {
      requestFailed: "请求失败",
      scanFailed: "扫描失败",
      scanQueued: (id) => `扫描 ${id} 已排队`,
      noScanSelected: "未选择扫描",
      analysisCompleted: (edges, candidates) =>
        `分析完成：${edges} 条关系，${candidates} 个候选`,
      analysisFailed: "分析失败",
      resourceLoadFailed: "资源加载失败",
      selectOpenCandidate: "请至少选择一个待处理候选",
      planCreated: (id) => `演练计划 ${id} 已创建`,
      planCreationFailed: "计划创建失败",
      planLoadFailed: "计划加载失败",
      planApproved: (id) => `计划 ${id} 已审批`,
      planApprovalFailed: "计划审批失败",
      planExecuted: (id) => `计划 ${id} 已执行`,
      planExecutionFailed: "计划执行失败",
      candidateUpdateFailed: "候选更新失败",
      selectCandidateAria: (name) => `选择 ${name}`,
    },
  },
};

export function detectLocale(
  languages: readonly string[] = browserLanguages(),
): Locale {
  for (const language of languages) {
    const normalized = language.toLowerCase().replace("_", "-");
    if (normalized === "zh" || normalized.startsWith("zh-")) {
      return "zh-CN";
    }
  }
  return "en-US";
}

export function copyForBrowser(): Copy {
  return copies[detectLocale()];
}

function browserLanguages(): readonly string[] {
  if (typeof navigator === "undefined") return [];
  return navigator.languages?.length
    ? navigator.languages
    : [navigator.language];
}
