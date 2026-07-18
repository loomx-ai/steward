import type { Asset, ResourceKind } from "@/api/types";

export interface ResourcePropertyRow {
  path: string;
  label: string;
  value: unknown;
}

export function resourceIdForProperties(
  name: string | undefined,
  nativeId: string,
): string {
  const trimmedName = name?.trim();
  const trimmedNativeId = nativeId.trim();
  return trimmedName && trimmedName === trimmedNativeId ? "" : trimmedNativeId;
}

const hiddenPropertyTokens = new Set([
  "accountid",
  "deleted",
  "inventorysource",
  "location",
  "region",
  "regionid",
  "state",
  "status",
]);
const timePropertyTokens = new Set([
  "createtime",
  "creationtime",
  "createdat",
  "expiretime",
  "expirationtime",
  "expiresat",
  "expiredat",
  "updatetime",
  "updatedat",
  "firstseenat",
  "lastseenat",
]);
const propertyPriorities = new Map([
  ["name", 0],
  ["vpcid", 1],
  ["vswitchid", 2],
  ["subnetid", 2],
  ["zoneid", 3],
  ["availabilityzone", 3],
  ["resourcegroupid", 4],
  ["tags", 5],
]);
const fallbackPropertyLabels: Record<
  string,
  Record<"en-US" | "zh-CN", string>
> = {
  name: { "en-US": "Name", "zh-CN": "名称" },
  vpcid: { "en-US": "VPC", "zh-CN": "所属专有网络" },
  vswitchid: { "en-US": "vSwitch", "zh-CN": "所属交换机" },
  subnetid: { "en-US": "Subnet", "zh-CN": "所属子网" },
  zoneid: { "en-US": "Zone", "zh-CN": "可用区" },
  availabilityzone: { "en-US": "Availability zone", "zh-CN": "可用区" },
  resourcegroupid: {
    "en-US": "Resource group ID",
    "zh-CN": "资源组 ID",
  },
  tags: { "en-US": "Tags", "zh-CN": "标签" },
  createtime: { "en-US": "Creation time", "zh-CN": "创建时间" },
  creationtime: { "en-US": "Creation time", "zh-CN": "创建时间" },
  createdat: { "en-US": "Created", "zh-CN": "创建时间" },
  expiretime: { "en-US": "Expiration time", "zh-CN": "到期时间" },
  expirationtime: { "en-US": "Expiration time", "zh-CN": "到期时间" },
  expiresat: { "en-US": "Expires", "zh-CN": "到期时间" },
  expiredat: { "en-US": "Expired", "zh-CN": "到期时间" },
  updatetime: { "en-US": "Update time", "zh-CN": "更新时间" },
  updatedat: { "en-US": "Updated", "zh-CN": "更新时间" },
  firstseenat: { "en-US": "First seen", "zh-CN": "首次发现时间" },
  lastseenat: { "en-US": "Last seen", "zh-CN": "最后发现时间" },
  associatetype: { "en-US": "Association type", "zh-CN": "关联类型" },
  bid: { "en-US": "Business ID", "zh-CN": "业务 ID" },
  dedup: { "en-US": "Deduplication", "zh-CN": "去重" },
  descritpion: { "en-US": "Description", "zh-CN": "描述" },
  destinationcidrblock: {
    "en-US": "Destination CIDR block",
    "zh-CN": "目标网段",
  },
  independentnaming: { "en-US": "Independent naming", "zh-CN": "独立命名" },
  mobilephone: { "en-US": "Mobile phone", "zh-CN": "手机号码" },
  msgretain: { "en-US": "Message retention", "zh-CN": "消息保留时长" },
  mutriorsignle: { "en-US": "Instance mode", "zh-CN": "实例形态" },
  netmode: { "en-US": "Network mode", "zh-CN": "网络模式" },
  nexthop: { "en-US": "Next hop", "zh-CN": "下一跳" },
  nexthops: { "en-US": "Next hops", "zh-CN": "下一跳" },
  nexthoptype: { "en-US": "Next-hop type", "zh-CN": "下一跳类型" },
  orca: { "en-US": "ORCA optimizer", "zh-CN": "ORCA 优化器" },
  ownerid: { "en-US": "Owner ID", "zh-CN": "所有者 ID" },
  pid: { "en-US": "PID", "zh-CN": "应用 PID" },
  portable: { "en-US": "Portable", "zh-CN": "是否可卸载" },
  profile: { "en-US": "Profile", "zh-CN": "集群类型" },
  routeentries: { "en-US": "Route entries", "zh-CN": "路由条目" },
  routeentrys: { "en-US": "Route entries", "zh-CN": "路由条目" },
  routepropagationenable: {
    "en-US": "Route propagation",
    "zh-CN": "路由传播",
  },
  routerid: { "en-US": "Router ID", "zh-CN": "路由器 ID" },
  routertype: { "en-US": "Router type", "zh-CN": "路由器类型" },
  routetableid: { "en-US": "Route table ID", "zh-CN": "路由表 ID" },
  routtablename: { "en-US": "Route table name", "zh-CN": "路由表名称" },
  routetabletype: { "en-US": "Route table type", "zh-CN": "路由表类型" },
  rpo: { "en-US": "RPO", "zh-CN": "恢复点目标（RPO）" },
  sassc: {
    "en-US": "Security Center switch",
    "zh-CN": "云安全中心开关",
  },
  saswebguardboolean: {
    "en-US": "Web tamper protection",
    "zh-CN": "网页防篡改开关",
  },
  sourcecidrblock: { "en-US": "Source CIDR block", "zh-CN": "源网段" },
  threatanalysisswitch1: {
    "en-US": "Threat analysis",
    "zh-CN": "威胁分析开关",
  },
  vrouterid: { "en-US": "Router ID", "zh-CN": "路由器 ID" },
  vpcids: { "en-US": "VPCs", "zh-CN": "所属专有网络" },
  vswitchids: { "en-US": "vSwitches", "zh-CN": "所属交换机" },
};

const zhPropertyWords: Record<string, string> = {
  accept: "接受",
  accelerator: "加速器",
  acl: "访问控制",
  access: "访问",
  action: "操作",
  activated: "激活",
  activation: "激活",
  add: "添加",
  added: "新增",
  accepting: "接收方",
  account: "账号",
  active: "活动",
  addon: "组件",
  addons: "组件",
  additional: "附加",
  administration: "管理",
  advanced: "高级",
  affinity: "亲和性",
  ai: "AI",
  ali: "阿里云",
  alias: "别名",
  alibaba: "阿里云",
  aliyun: "阿里云",
  allocation: "分配",
  address: "地址",
  algorithm: "算法",
  allow: "允许",
  allowed: "允许",
  anycast: "任播",
  app: "应用",
  application: "应用",
  arch: "架构",
  area: "区域",
  arg: "参数",
  args: "参数",
  amount: "数量",
  api: "API",
  arn: "ARN",
  array: "数组",
  asn: "ASN",
  associate: "关联",
  associated: "关联",
  assurance: "保障",
  attach: "挂载",
  attached: "挂载",
  attachment: "挂载",
  attribute: "属性",
  auth: "认证",
  architecture: "架构",
  auto: "自动",
  automatic: "自动",
  autoscaling: "自动伸缩",
  available: "可用",
  availability: "可用性",
  az: "可用区",
  backup: "备份",
  backend: "后端",
  balance: "均衡",
  balancer: "负载均衡器",
  bandwidth: "带宽",
  base: "基础",
  bind: "绑定",
  biz: "业务",
  billing: "计费",
  block: "阻止",
  blocks: "网段",
  boot: "启动",
  border: "边界",
  bps: "BPS",
  bursting: "突发",
  bytes: "字节",
  bucket: "存储桶",
  business: "业务",
  cache: "缓存",
  caller: "调用方",
  can: "可",
  capacity: "容量",
  catalog: "目录",
  category: "类别",
  cdc: "CDC",
  cdn: "CDN",
  cen: "CEN",
  cert: "证书",
  certificate: "证书",
  charge: "计费",
  check: "检查",
  cidr: "CIDR",
  circuit: "线路",
  class: "规格",
  classic: "经典网络",
  client: "客户端",
  clock: "时钟",
  cloud: "云",
  cluster: "集群",
  cname: "CNAME",
  code: "代码",
  cold: "冷",
  column: "列",
  comment: "备注",
  comments: "备注",
  commodity: "商品",
  compact: "压缩",
  compress: "压缩",
  compression: "压缩",
  compute: "计算",
  computing: "计算",
  condition: "条件",
  consist: "一致性",
  consistency: "一致性",
  container: "容器",
  containers: "容器",
  content: "内容",
  controller: "控制器",
  config: "配置",
  configuration: "配置",
  configured: "已配置",
  connection: "连接",
  connections: "连接",
  control: "控制",
  copied: "已复制",
  copy: "复制",
  core: "核",
  cores: "核",
  cors: "跨域",
  cost: "成本",
  count: "数量",
  coverage: "覆盖范围",
  cpu: "CPU",
  create: "创建",
  created: "已创建",
  creation: "创建",
  creator: "创建者",
  criteria: "条件",
  cross: "跨",
  current: "当前",
  customer: "用户",
  data: "数据",
  database: "数据库",
  date: "日期",
  days: "天",
  db: "数据库",
  ddos: "DDoS",
  debt: "欠费",
  dedicated: "专有",
  default: "默认",
  delay: "延迟",
  delete: "删除",
  deletion: "删除",
  demand: "按需",
  deploy: "部署",
  deployment: "部署",
  desc: "描述",
  description: "描述",
  dest: "目标",
  desired: "期望",
  destination: "目标",
  detect: "检测",
  detection: "检测",
  device: "设备",
  dhcp: "DHCP",
  direct: "直连",
  directory: "目录",
  direction: "方向",
  disable: "禁用",
  disabled: "已禁用",
  disk: "磁盘",
  display: "显示名称",
  distributed: "分布式",
  dns: "DNS",
  docker: "Docker",
  document: "文档",
  domain: "域名",
  downstream: "下游",
  duration: "时长",
  duplication: "复制",
  egress: "出方向",
  eip: "弹性公网 IP",
  enable: "启用",
  enabled: "已启用",
  endpoint: "端点",
  endpoints: "端点",
  engine: "引擎",
  eni: "弹性网卡",
  encryption: "加密",
  encrypt: "加密",
  encrypted: "已加密",
  empty: "空",
  enterprise: "企业",
  entries: "条目",
  entry: "条目",
  entrys: "条目",
  env: "环境",
  environment: "环境",
  ecs: "ECS",
  edition: "版本",
  effect: "效果",
  elastic: "弹性",
  elasticity: "弹性保障",
  email: "邮箱",
  end: "结束",
  error: "错误",
  es: "Elasticsearch",
  event: "事件",
  events: "事件",
  eviction: "回收",
  execution: "执行",
  exclude: "排除",
  expire: "到期",
  expired: "已到期",
  expiration: "到期",
  expiring: "即将到期",
  extend: "扩展",
  extended: "扩展",
  external: "外部",
  extranet: "公网",
  extra: "扩展",
  failed: "失败",
  family: "系列",
  feature: "功能",
  features: "功能",
  file: "文件",
  finger: "指纹",
  fingerprint: "指纹",
  first: "首次",
  flow: "流量",
  force: "强制",
  format: "格式",
  forward: "转发",
  free: "免费",
  fqdn: "FQDN",
  full: "全",
  function: "函数",
  gateway: "网关",
  geographic: "地理",
  global: "全球",
  gmt: "GMT",
  gpu: "GPU",
  grant: "授权",
  group: "组",
  groups: "组",
  ha: "高可用",
  has: "是否有",
  hash: "哈希",
  header: "请求头",
  health: "健康检查",
  hibernation: "休眠",
  high: "高",
  honeypot: "蜜罐",
  hop: "下一跳",
  host: "主机",
  hostname: "主机名",
  hot: "热",
  http: "HTTP",
  https: "HTTPS",
  iasize: "IA 大小",
  icmp: "ICMP",
  image: "镜像",
  images: "镜像",
  import: "导入",
  in: "入",
  inactive: "非活动",
  include: "包含",
  index: "索引",
  info: "信息",
  ingress: "入方向",
  init: "初始",
  inner: "内部",
  inode: "Inode",
  instant: "即时",
  id: "ID",
  ids: "ID",
  identifier: "标识符",
  interface: "网卡",
  interfaces: "网卡",
  internal: "内部",
  inter: "公网",
  instance: "实例",
  internet: "公网",
  interval: "间隔",
  io: "IO",
  iops: "IOPS",
  intra: "内网",
  intranet: "内网",
  ip: "IP",
  ipv4: "IPv4",
  ipv6: "IPv6",
  is: "是否",
  isp: "运营商",
  item: "项目",
  items: "项目",
  key: "密钥",
  keys: "密钥",
  kibana: "Kibana",
  kms: "KMS",
  kernel: "内核",
  kube: "Kubernetes",
  kubernetes: "Kubernetes",
  last: "最后",
  latest: "最新",
  launch: "启动",
  lcu: "LCU",
  level: "级别",
  like: "匹配",
  lifecycle: "生命周期",
  license: "许可证",
  limit: "限制",
  limited: "限制",
  line: "线路",
  link: "链接",
  list: "列表",
  listener: "监听",
  load: "负载",
  lock: "锁定",
  local: "本地",
  location: "位置",
  log: "日志",
  logging: "日志",
  login: "登录",
  long: "长整型",
  mac: "MAC",
  maintain: "维护",
  managed: "托管",
  manager: "管理",
  mapping: "映射",
  mask: "掩码",
  master: "主",
  match: "匹配",
  material: "证书",
  max: "最大",
  maximum: "最大",
  mbps: "Mbps",
  memory: "内存",
  message: "消息",
  messages: "消息",
  metadata: "元数据",
  metered: "计量",
  min: "最小",
  minutes: "分钟",
  minor: "小版本",
  mode: "模式",
  models: "型号",
  modification: "修改",
  modified: "修改",
  modify: "修改",
  monitor: "监控",
  mount: "挂载",
  multi: "多",
  multicast: "组播",
  nat: "NAT",
  name: "名称",
  namespace: "命名空间",
  network: "网络",
  net: "网络",
  next: "下一",
  node: "节点",
  nodes: "节点",
  nic: "网卡",
  not: "不",
  notify: "通知",
  numa: "NUMA",
  num: "数量",
  nums: "数量",
  number: "数量",
  object: "对象",
  odps: "MaxCompute",
  on: "开启",
  online: "在线",
  only: "只读",
  open: "开源",
  operation: "操作",
  optimized: "优化",
  options: "选项",
  order: "订单",
  organization: "组织",
  origin: "源站",
  os: "操作系统",
  oss: "OSS",
  out: "出",
  outputs: "输出",
  owner: "所有者",
  package: "套餐",
  pair: "对",
  param: "参数",
  parameter: "参数",
  parent: "父级",
  payment: "付费",
  pay: "付费",
  peering: "对等连接",
  pending: "等待",
  performance: "性能",
  period: "周期",
  permission: "权限",
  permissions: "权限",
  persist: "持久化",
  physical: "物理",
  platform: "平台",
  placement: "部署集",
  task: "任务",
  pod: "Pod",
  point: "时间点",
  policy: "策略",
  polling: "轮询",
  pool: "池",
  port: "端口",
  ports: "端口",
  prefix: "前缀",
  premium: "高级",
  primary: "主",
  priority: "优先级",
  principal: "主体",
  private: "私网",
  process: "进程",
  processes: "进程",
  product: "产品",
  progress: "进度",
  project: "项目",
  protection: "保护",
  protocol: "协议",
  provider: "云厂商",
  provision: "预置",
  provisioned: "预置",
  proxy: "代理",
  public: "公网",
  pull: "拉取",
  push: "推送",
  qps: "QPS",
  quantity: "数量",
  queue: "队列",
  quota: "配额",
  ram: "RAM",
  ratio: "比例",
  range: "范围",
  read: "读",
  readiness: "就绪",
  readonly: "只读",
  real: "实际",
  reason: "原因",
  receive: "接收",
  record: "记录",
  recover: "恢复",
  recovery: "恢复",
  recyclable: "可回收",
  redundancy: "冗余",
  referer: "来源",
  region: "地域",
  registrant: "注册人",
  registration: "注册",
  release: "释放",
  removal: "移除",
  removing: "移除中",
  remote: "远程",
  remark: "备注",
  renew: "续费",
  renewal: "续费",
  repeat: "重复",
  replica: "副本",
  replicas: "副本",
  replication: "复制",
  request: "请求",
  requests: "请求",
  reservation: "预留",
  reserved: "预留",
  resident: "所在",
  resource: "资源",
  resources: "资源",
  response: "响应",
  restart: "重启",
  retention: "保留",
  retry: "重试",
  role: "角色",
  root: "根",
  rotation: "轮转",
  rule: "规则",
  rules: "规则",
  running: "运行中",
  runtime: "运行时",
  safety: "安全",
  sale: "售卖",
  sandbox: "沙箱",
  scaling: "伸缩",
  scale: "扩缩容",
  scheduler: "调度",
  scope: "范围",
  search: "搜索",
  second: "秒",
  secondary: "辅助",
  secure: "安全",
  segment: "分片",
  send: "发送",
  serial: "序列号",
  series: "系列",
  route: "路由",
  router: "路由器",
  security: "安全",
  server: "服务器",
  servers: "服务器",
  service: "服务",
  session: "会话",
  set: "集合",
  shard: "分片",
  share: "共享",
  show: "显示",
  side: "端",
  single: "单",
  site: "站点",
  size: "大小",
  slave: "从",
  slb: "负载均衡",
  sls: "SLS",
  sn: "序列号",
  snapshot: "快照",
  source: "源",
  space: "空间",
  spec: "规格",
  specification: "规格",
  spot: "抢占式",
  sql: "SQL",
  ssl: "SSL",
  stack: "资源栈",
  standby: "备用",
  start: "开始",
  statement: "声明",
  status: "状态",
  state: "状态",
  stopped: "已停止",
  storage: "存储",
  store: "库",
  strategy: "策略",
  stream: "流",
  strict: "强一致",
  string: "字符串",
  sub: "子",
  subscribed: "已订阅",
  subnet: "子网",
  super: "超级",
  support: "支持",
  supported: "支持",
  suspended: "已暂停",
  switch: "交换机",
  sync: "同步",
  system: "系统",
  table: "表",
  tables: "表",
  tag: "标签",
  tags: "标签",
  taint: "污点",
  tcp: "TCP",
  template: "模板",
  tenancy: "宿主机类型",
  tenant: "租户",
  termination: "终止",
  thread: "线程",
  throughput: "吞吐量",
  time: "时间",
  timeout: "超时",
  timestamp: "时间戳",
  timezone: "时区",
  topic: "主题",
  total: "总计",
  trace: "链路追踪",
  traffic: "流量",
  trail: "跟踪",
  transit: "转发",
  trial: "试用",
  tunnel: "隧道",
  type: "类型",
  uid: "UID",
  unique: "唯一",
  unit: "单位",
  update: "更新",
  updated: "更新",
  upgrade: "升级",
  upper: "上限",
  url: "URL",
  urls: "URL",
  usable: "可用",
  use: "使用",
  usage: "用途",
  using: "使用",
  used: "已使用",
  user: "用户",
  value: "值",
  vault: "存储库",
  vbr: "边界路由器",
  version: "版本",
  vip: "VIP",
  vips: "VIP",
  virtual: "虚拟",
  visibility: "可见性",
  vlan: "VLAN",
  volume: "卷",
  vpc: "专有网络",
  vswitch: "交换机",
  vsw: "交换机",
  wait: "等待",
  warning: "告警",
  watch: "监听",
  webhook: "Webhook",
  weight: "权重",
  weighted: "加权",
  white: "白名单",
  whitelist: "白名单",
  worker: "工作节点",
  workers: "工作节点",
  workspace: "工作空间",
  write: "写",
  zone: "可用区",
  zones: "可用区",
};

export function resourcePropertyRows(
  asset: Asset,
  kind: ResourceKind | undefined,
  locale: string,
  resourceId = asset.identity.native_id,
): ResourcePropertyRow[] {
  const normalized = asset.normalized ?? {};
  const configurationPaths = configurationPropertyPaths(
    normalized.configuration,
  );
  const declaredPaths =
    kind?.properties?.map((property) => property.path) ??
    kind?.summary_fields ??
    [];
  const summaryOrder = new Map(
    declaredPaths.map((path, index) => [path, index]),
  );
  const paths = [
    ...declaredPaths.filter((path) => hasPath(normalized, path)),
    ...Object.keys(normalized).filter(
      (path) => path !== "configuration" && !summaryOrder.has(path),
    ),
    ...configurationPaths.filter((path) => !summaryOrder.has(path)),
  ].filter((path, index, values) => values.indexOf(path) === index);

  paths.sort((left, right) => {
    const priorityDifference =
      propertyPriority(left, summaryOrder) -
      propertyPriority(right, summaryOrder);
    if (priorityDifference !== 0) return priorityDifference;
    const leftConfiguration = left.startsWith("configuration.");
    const rightConfiguration = right.startsWith("configuration.");
    if (leftConfiguration !== rightConfiguration) {
      return leftConfiguration ? 1 : -1;
    }
    const summaryDifference =
      (summaryOrder.get(left) ?? Number.MAX_SAFE_INTEGER) -
      (summaryOrder.get(right) ?? Number.MAX_SAFE_INTEGER);
    if (summaryDifference !== 0) return summaryDifference;
    return left.localeCompare(right, locale);
  });

  const seenTokens = new Set<string>();
  const propertyRows = paths.flatMap((path) => {
    const token = propertyToken(path);
    const semanticToken = propertySemanticToken(token);
    const value = readPath(normalized, path);
    if (isNativeIDValue(token, value, asset, resourceId)) {
      return [];
    }
    if (
      path.startsWith("configuration.") &&
      isProjectedNameValue(token, value, normalized, asset)
    ) {
      return [];
    }
    if (isHiddenProperty(token) || isEmptyPropertyValue(value)) {
      return [];
    }
    if (seenTokens.has(semanticToken)) return [];
    seenTokens.add(semanticToken);
    return [
      {
        path,
        label: resourcePropertyLabel(path, kind, locale),
        value,
      },
    ];
  });
  return [...resourceIdentityRows(resourceId), ...propertyRows];
}

export function displayBrowserTime(
  value: unknown,
  formatDate: (value: string | Date) => string,
): string | undefined {
  if (
    typeof value !== "string" &&
    typeof value !== "number" &&
    !(value instanceof Date)
  ) {
    return undefined;
  }
  const date =
    value instanceof Date
      ? value
      : typeof value === "number"
        ? new Date(value < 1_000_000_000_000 ? value * 1_000 : value)
        : new Date(value);
  if (Number.isNaN(date.getTime())) return undefined;
  return formatDate(date);
}

export function displayPropertyValue(value: unknown): string {
  if (value === undefined || value === null || value === "") return "";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

export function isTagProperty(path: string): boolean {
  return ["tags", "tagset", "tagresources"].includes(propertyToken(path));
}

export function isTimeProperty(path: string): boolean {
  const token = propertyToken(path);
  return (
    timePropertyTokens.has(token) ||
    token.endsWith("time") ||
    token.endsWith("date") ||
    token.endsWith("timestamp")
  );
}

export function isResourcePropertyHidden(path: string): boolean {
  return isHiddenProperty(propertyToken(path));
}

export function tagValues(
  value: unknown,
  locale: string,
): Array<{ key: string; label: string }> {
  if (Array.isArray(value)) {
    return value.flatMap((tag, index) => {
      const normalized = normalizedTag(tag, index);
      return normalized ? [normalized] : [];
    });
  }
  if (!value || typeof value !== "object") return [];
  const nestedTags = Object.entries(value).find(([key, nested]) => {
    const token = propertyToken(key);
    return (
      Array.isArray(nested) &&
      ["tag", "tags", "tagset", "tagresources"].includes(token)
    );
  });
  if (nestedTags && Array.isArray(nestedTags[1])) {
    return nestedTags[1].flatMap((tag, index) => {
      const normalized = normalizedTag(tag, index);
      return normalized ? [normalized] : [];
    });
  }
  return Object.entries(value)
    .sort(([left], [right]) => left.localeCompare(right, locale))
    .map(([key, tagValue]) => {
      const valueText = displayPropertyValue(tagValue);
      return {
        key,
        label:
          tagValue === "" || tagValue === null || tagValue === undefined
            ? key
            : `${key}: ${valueText}`,
      };
    });
}

export function resourcePropertyLabel(
  path: string,
  kind: ResourceKind | undefined,
  locale: string,
): string {
  const token = propertyToken(path);
  const explicit = kind?.field_display_names?.[path]?.[locale]?.trim();
  if (explicit) return explicit;
  const fallback = fallbackPropertyLabel(token, locale);
  if (fallback) return fallback;
  return humanizePropertyName(path.split(".").at(-1) ?? path, locale);
}

function propertyPriority(
  path: string,
  summaryOrder: ReadonlyMap<string, number>,
): number {
  const token = propertySemanticToken(propertyToken(path));
  if (isTimeProperty(path)) return 100;
  if (token.endsWith("name")) return 0;
  return propertyPriorities.get(token) ?? (summaryOrder.has(path) ? 20 : 30);
}

function resourceIdentityRows(
  resourceId: string | undefined,
): ResourcePropertyRow[] {
  const value = resourceId?.trim();
  return value ? [{ path: "$nativeId", label: "ID", value }] : [];
}

function propertyToken(path: string): string {
  return (path.split(".").at(-1) ?? path)
    .toLocaleLowerCase("en-US")
    .replaceAll(/[^a-z0-9]/g, "");
}

function propertySemanticToken(token: string): string {
  if (token === "vpcids") return "vpcid";
  if (["vswitchids", "vswids"].includes(token)) return "vswitchid";
  if (token === "subnetids") return "subnetid";
  if (token === "zoneids") return "zoneid";
  if (token === "vrouterid") return "routerid";
  if (
    [
      "createdate",
      "createdat",
      "createtime",
      "createtimestamp",
      "creationdate",
      "creationtime",
    ].includes(token)
  ) {
    return "creationtime";
  }
  if (
    [
      "lastmodified",
      "lastmodifiedtime",
      "lastmodifytime",
      "modificationtime",
      "modifiedat",
      "modifiedtime",
      "modifytime",
      "updated",
      "updatedate",
      "updatetime",
      "updatetimestamp",
      "updatedat",
      "updatedtime",
    ].includes(token)
  ) {
    return "updatetime";
  }
  if (
    [
      "expirationdate",
      "expirationdatelong",
      "expirationtime",
      "expiredate",
      "expiredatetime",
      "expiretime",
      "expiredat",
      "expiredtime",
      "expiresat",
    ].includes(token)
  ) {
    return "expirationtime";
  }
  if (["tagresources", "tagset", "tags"].includes(token)) return "tags";
  return token;
}

function isHiddenProperty(token: string): boolean {
  return (
    hiddenPropertyTokens.has(token) ||
    token.endsWith("status") ||
    token.endsWith("state")
  );
}

function isNativeIDValue(
  token: string,
  value: unknown,
  asset: Asset,
  resourceId: string | undefined,
): boolean {
  if (typeof value !== "string" && typeof value !== "number") return false;
  const text = String(value).trim();
  if (!text) return false;
  if (!token.endsWith("id")) return false;
  return [asset.identity.native_id.trim(), resourceId?.trim()].includes(text);
}

function isProjectedNameValue(
  token: string,
  value: unknown,
  normalized: Record<string, unknown>,
  asset: Asset,
): boolean {
  if (typeof value !== "string" && typeof value !== "number") return false;
  const text = String(value).trim();
  if (!text) return false;
  if (!token.endsWith("name")) return false;
  const names = [normalized.name, asset.name]
    .filter(
      (candidate): candidate is string | number =>
        typeof candidate === "string" || typeof candidate === "number",
    )
    .map((candidate) => String(candidate).trim());
  return names.includes(text);
}

function fallbackPropertyLabel(token: string, locale: string): string {
  const labels = fallbackPropertyLabels[token];
  if (!labels) return "";
  return labels[locale === "zh-CN" ? "zh-CN" : "en-US"];
}

function configurationPropertyPaths(value: unknown): string[] {
  if (!value || typeof value !== "object" || Array.isArray(value)) return [];
  return Object.keys(value).map((key) => `configuration.${key}`);
}

function humanizePropertyName(value: string, locale: string): string {
  const words = propertyWords(value);
  if (locale === "zh-CN") {
    const translated = words.map((word) => translatePropertyWord(word) ?? word);
    if (translated.some((word, index) => word !== words[index])) {
      return translated.join("");
    }
  }
  const label = words.join(" ");
  return label ? label[0].toLocaleUpperCase("en-US") + label.slice(1) : value;
}

function translatePropertyWord(word: string): string | undefined {
  const token = word.toLocaleLowerCase("en-US");
  const exact = zhPropertyWords[token];
  if (exact) return exact;
  const singularCandidates = token.endsWith("ies")
    ? [`${token.slice(0, -3)}y`]
    : token.endsWith("es")
      ? [token.slice(0, -2), token.slice(0, -1)]
      : token.endsWith("s")
        ? [token.slice(0, -1)]
        : [];
  for (const candidate of singularCandidates) {
    const translated = zhPropertyWords[candidate];
    if (translated) return translated;
  }
  return undefined;
}

function propertyWords(value: string): string[] {
  return value
    .replaceAll("VSwitch", "Vswitch")
    .replaceAll(/([A-Z]+)([A-Z][a-z])/g, "$1 $2")
    .replaceAll(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replaceAll(/[_-]+/g, " ")
    .trim()
    .split(/\s+/u)
    .filter(Boolean)
    .map((word) => {
      const token = word.toLocaleLowerCase("en-US");
      if (token === "id" || token === "ids") return token.toUpperCase();
      if (token === "url" || token === "urls") return token.toUpperCase();
      if (token === "api" || token === "arn" || token === "acl")
        return token.toUpperCase();
      if (token === "cpu" || token === "gpu" || token === "ram")
        return token.toUpperCase();
      if (token === "http" || token === "https" || token === "ssl")
        return token.toUpperCase();
      if (token === "ip" || token === "ipv4" || token === "ipv6")
        return token === "ip" ? "IP" : `IPv${token.slice(3)}`;
      if (token === "vpc") return "VPC";
      if (token === "vswitch") return "vSwitch";
      return word;
    });
}

function normalizedTag(
  value: unknown,
  index: number,
): { key: string; label: string } | undefined {
  if (
    !value ||
    typeof value === "string" ||
    typeof value === "number" ||
    typeof value === "boolean"
  ) {
    const label = displayPropertyValue(value);
    return { key: `${index}:${label}`, label };
  }
  if (Array.isArray(value)) return undefined;
  const entries = value as Record<string, unknown>;
  const tagKey =
    entries.Key ?? entries.TagKey ?? entries.key ?? entries.tagKey ?? "";
  const tagValue =
    entries.Value ?? entries.TagValue ?? entries.value ?? entries.tagValue;
  if (typeof tagKey === "string" && tagKey.trim()) {
    const text = displayPropertyValue(tagValue);
    return {
      key: `${index}:${tagKey}`,
      label: text ? `${tagKey}: ${text}` : tagKey,
    };
  }
  return undefined;
}

function isEmptyPropertyValue(value: unknown): boolean {
  if (value === undefined || value === null) return true;
  if (typeof value === "string") return value.trim() === "";
  if (Array.isArray(value)) {
    return value.length === 0 || value.every(isEmptyPropertyValue);
  }
  if (typeof value === "object") {
    const values = Object.values(value);
    return values.length === 0 || values.every(isEmptyPropertyValue);
  }
  return false;
}

function hasPath(
  value: Record<string, unknown> | undefined,
  path: string,
): boolean {
  return readPath(value, path) !== undefined;
}

function readPath(
  value: Record<string, unknown> | undefined,
  path: string,
): unknown {
  let current: unknown = value;
  for (const segment of path.split(".")) {
    if (!current || typeof current !== "object" || Array.isArray(current)) {
      return undefined;
    }
    current = (current as Record<string, unknown>)[segment];
  }
  return current;
}
