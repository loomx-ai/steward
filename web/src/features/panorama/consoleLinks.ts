export interface CloudConsoleResource {
  provider: string;
  nativeType: string;
  nativeId: string;
  regionId?: string;
  consoleLinkTemplate?: string;
  templateValues?: Record<string, unknown>;
}

export function cloudConsoleURL(
  resource: CloudConsoleResource,
): string | undefined {
  const provider = resource.provider?.trim().toLowerCase() ?? "";
  const nativeType = resource.nativeType?.trim() ?? "";
  const nativeId = resource.nativeId?.trim() ?? "";
  const regionId = resource.regionId?.trim() ?? "";
  if (!nativeType || !nativeId) return undefined;

  if (resource.consoleLinkTemplate) {
    return expandConsoleLinkTemplate(
      resource.consoleLinkTemplate,
      nativeId,
      regionId,
      resource.templateValues,
    );
  }
  if (provider === "aws") {
    return awsConsoleURL(nativeType, nativeId, regionId);
  }
  return undefined;
}

function expandConsoleLinkTemplate(
  template: string,
  nativeId: string,
  regionId: string,
  templateValues?: Record<string, unknown>,
) {
  const values: Record<string, string> = {};
  for (const [key, raw] of Object.entries(templateValues ?? {})) {
    if (typeof raw === "string") {
      values[key] = raw.trim();
    } else if (typeof raw === "number" && Number.isFinite(raw)) {
      values[key] = String(raw);
    }
  }
  values.nativeId = nativeId;
  values.regionId = regionId || values.regionId || "";
  let missingValue = false;
  const expanded = template.replace(
    /\{([A-Za-z][A-Za-z0-9]*)(?:\|([A-Za-z][A-Za-z0-9]*)(?::([^{}]*))?)?\}/g,
    (_, key: string, filter?: string, argument?: string) => {
      const source = values[key];
      const value = applyConsoleLinkFilter(source, filter, argument);
      if (!value) missingValue = true;
      return encodeURIComponent(value ?? "");
    },
  );
  if (
    missingValue ||
    /[{}]/.test(expanded) ||
    !expanded.startsWith("https://")
  ) {
    return undefined;
  }
  try {
    const url = new URL(expanded);
    return url.protocol === "https:" ? url.toString() : undefined;
  } catch {
    return undefined;
  }
}

function applyConsoleLinkFilter(
  value: string | undefined,
  filter?: string,
  argument?: string,
) {
  if (!value) return undefined;
  if (!filter) return value;
  if (filter === "suffix" && argument) {
    const separator = value.lastIndexOf(argument);
    return separator >= 0 ? value.slice(separator + argument.length) : value;
  }
  if (filter === "replacePrefix" && argument) {
    const [oldPrefix, newPrefix, ...extra] = argument.split(",");
    if (!oldPrefix || !newPrefix || extra.length > 0) return undefined;
    return value.startsWith(oldPrefix)
      ? `${newPrefix}${value.slice(oldPrefix.length)}`
      : value;
  }
  return undefined;
}

function awsConsoleURL(
  nativeType: string,
  nativeId: string,
  fallbackRegion: string,
) {
  const arn = parseARN(nativeId);
  const region = arn?.region || fallbackRegion;
  const physicalId = arn?.physicalId || nativeId;
  const query = new URLSearchParams(region ? { region } : {});
  const suffix = query.size > 0 ? `?${query.toString()}` : "";

  switch (nativeType) {
    case "AWS::EC2::VPC":
      if (!region) return undefined;
      return `https://console.aws.amazon.com/vpcconsole/home${suffix}#VpcDetails:VpcId=${encodeURIComponent(physicalId)}`;
    case "AWS::EC2::Subnet":
      if (!region) return undefined;
      return `https://console.aws.amazon.com/vpcconsole/home${suffix}#SubnetDetails:subnetId=${encodeURIComponent(physicalId)}`;
    case "AWS::EC2::Instance":
      return ec2ConsoleURL(region, "InstanceDetails:instanceId", physicalId);
    case "AWS::EC2::Volume":
      return ec2ConsoleURL(region, "VolumeDetails:volumeId", physicalId);
    case "AWS::EC2::Snapshot":
      return ec2ConsoleURL(region, "SnapshotDetails:snapshotId", physicalId);
    case "AWS::EC2::SecurityGroup":
      return ec2ConsoleURL(region, "SecurityGroup:groupId", physicalId);
    case "AWS::EC2::NetworkInterface":
      return ec2ConsoleURL(
        region,
        "NetworkInterface:networkInterfaceId",
        physicalId,
      );
    case "AWS::CloudFormation::Stack": {
      if (!region) return undefined;
      const stackQuery = new URLSearchParams({ region });
      return `https://console.aws.amazon.com/cloudformation/home?${stackQuery.toString()}#/stacks/stackinfo?stackId=${encodeURIComponent(nativeId)}`;
    }
    case "AWS::IAM::Role":
      return `https://console.aws.amazon.com/iam/home#/roles/details/${pathSegment(physicalId)}`;
    case "AWS::S3::Bucket":
      return `https://s3.console.aws.amazon.com/s3/buckets/${pathSegment(physicalId)}`;
    default:
      return awsResourceExplorerURL(nativeType, nativeId, region);
  }
}

function awsResourceExplorerURL(
  nativeType: string,
  nativeId: string,
  region: string,
) {
  const query = nativeId.startsWith("arn:")
    ? `id:${nativeId}`
    : `resourcetype:${nativeType} ${nativeId}`;
  const parameters = new URLSearchParams({ region: region || "us-east-1" });
  return `https://resource-explorer.console.aws.amazon.com/resource-explorer/home?${parameters.toString()}#/search?query=${encodeURIComponent(query)}`;
}

function ec2ConsoleURL(region: string, detail: string, physicalId: string) {
  if (!region) return undefined;
  const query = new URLSearchParams({ region });
  return `https://console.aws.amazon.com/ec2/home?${query.toString()}#${detail}=${encodeURIComponent(physicalId)}`;
}

function parseARN(value: string) {
  const parts = value.split(":", 6);
  if (parts.length !== 6 || parts[0] !== "arn") return undefined;
  const resource = parts[5].replace(/^[/]+/, "");
  const separator = Math.max(
    resource.lastIndexOf("/"),
    resource.lastIndexOf(":"),
  );
  return {
    region: parts[3],
    physicalId: separator >= 0 ? resource.slice(separator + 1) : resource,
  };
}

function pathSegment(value: string) {
  return encodeURIComponent(value.trim());
}
