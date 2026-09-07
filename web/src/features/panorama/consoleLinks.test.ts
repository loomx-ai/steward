import { readdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { cloudConsoleURL } from "./consoleLinks";

interface ProviderCatalog {
  resource_types: Array<{ native_type: string }>;
}

const awsCatalog = JSON.parse(
  readFileSync(
    resolve(process.cwd(), "../providers/aws/catalog/generated/catalog.json"),
    "utf8",
  ).replace(/^\/\/[^\n]*\n/, ""),
) as ProviderCatalog;
const alicloudSpecs = readdirSync(
  resolve(process.cwd(), "../providers/alicloud/specs"),
)
  .filter((name) => name.endsWith(".yaml"))
  .map((name) => {
    const source = readFileSync(
      resolve(process.cwd(), "../providers/alicloud/specs", name),
      "utf8",
    );
    return {
      name,
      nativeType: source.match(/^\s*nativeType:\s*(\S+)\s*$/m)?.[1] ?? "",
      template:
        source.match(/^\s*consoleLinkTemplate:\s*["']([^"']+)["']\s*$/m)?.[1] ??
        "",
    };
  });

describe("cloudConsoleURL", () => {
  it("expands provider-spec templates for Alibaba Cloud detail routes", () => {
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::ROS::Stack",
        nativeId: "bc95e08e-0d29-4315-9ed2-125cd57fed76",
        regionId: "cn-wulanchabu-gic-1",
        consoleLinkTemplate:
          "https://ros.console.aliyun.com/{regionId}/stacks/{nativeId}",
      }),
    ).toBe(
      "https://ros.console.aliyun.com/cn-wulanchabu-gic-1/stacks/bc95e08e-0d29-4315-9ed2-125cd57fed76",
    );
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::ECS::SecurityGroup",
        nativeId: "sg-bp15nz72t4melig8sybz",
        regionId: "cn-hangzhou",
        consoleLinkTemplate:
          "https://ecs.console.aliyun.com/securityGroupDetail/region/{regionId}/groupId/{nativeId}/detail/intranetIngress",
      }),
    ).toBe(
      "https://ecs.console.aliyun.com/securityGroupDetail/region/cn-hangzhou/groupId/sg-bp15nz72t4melig8sybz/detail/intranetIngress",
    );
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::ECS::AutoSnapshotPolicy",
        nativeId: "sp-k8ih6tvzeq0wwo04n9pi",
        regionId: "cn-hangzhou-acdr-ut-1",
        consoleLinkTemplate:
          "https://ecs.console.aliyun.com/autoSnapshotPolicyDetail/{regionId}/{nativeId}/detail",
      }),
    ).toBe(
      "https://ecs.console.aliyun.com/autoSnapshotPolicyDetail/cn-hangzhou-acdr-ut-1/sp-k8ih6tvzeq0wwo04n9pi/detail",
    );
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::SLS::Project",
        nativeId: "log-service-1754580903499898-ap-southeast-7",
        regionId: "ap-southeast-7",
        consoleLinkTemplate:
          "https://sls.console.aliyun.com/lognext/project/{nativeId}/overview?slsRegion={regionId}",
      }),
    ).toBe(
      "https://sls.console.aliyun.com/lognext/project/log-service-1754580903499898-ap-southeast-7/overview?slsRegion=ap-southeast-7",
    );
  });

  it.each([
    {
      name: "RDS instance",
      nativeType: "ACS::RDS::DBInstance",
      nativeId: "rm-production",
      template:
        "https://rdsnext.console.aliyun.com/detail/{nativeId}/basicInfo?region={regionId}",
      expected:
        "https://rdsnext.console.aliyun.com/detail/rm-production/basicInfo?region=cn-hangzhou",
    },
    {
      name: "Redis instance",
      nativeType: "ACS::Redis::DBInstance",
      nativeId: "r-production",
      template:
        "https://kvstore.console.aliyun.com/Redis/instance/{regionId}/{nativeId}",
      expected:
        "https://kvstore.console.aliyun.com/Redis/instance/cn-hangzhou/r-production",
    },
    {
      name: "MongoDB instance",
      nativeType: "ACS::MongoDB::DBInstance",
      nativeId: "dds-production",
      template:
        "https://mongodb.console.aliyun.com/replicate/{regionId}/instances/{nativeId}/basicInfo",
      expected:
        "https://mongodb.console.aliyun.com/replicate/cn-hangzhou/instances/dds-production/basicInfo",
    },
    {
      name: "Elasticsearch instance",
      nativeType: "ACS::Elasticsearch::Instance",
      nativeId: "es-production",
      template:
        "https://elasticsearch.console.aliyun.com/{regionId}/instances/{nativeId}/base",
      expected:
        "https://elasticsearch.console.aliyun.com/cn-hangzhou/instances/es-production/base",
    },
    {
      name: "Logstash instance",
      nativeType: "ACS::Elasticsearch::Logstash",
      nativeId: "ls-production",
      template:
        "https://elasticsearch.console.aliyun.com/{regionId}/logstashes/{nativeId}/base",
      expected:
        "https://elasticsearch.console.aliyun.com/cn-hangzhou/logstashes/ls-production/base",
    },
    {
      name: "ROS stack group",
      nativeType: "ACS::ROS::StackGroup",
      nativeId: "tf-example2",
      regionId: "cn-beijing",
      template:
        "https://ros.console.aliyun.com/{regionId}/stackGroup/{nativeId}",
      expected:
        "https://ros.console.aliyun.com/cn-beijing/stackGroup/tf-example2",
    },
    {
      name: "ESS scaling group",
      nativeType: "ACS::ESS::ScalingGroup",
      nativeId: "asg-2ze43yvwsqeze3z5hkrh",
      regionId: "cn-beijing",
      template:
        "https://ess.console.aliyun.com/#/v3/group/detail/{regionId}/{nativeId}/basicInfo",
      expected:
        "https://ess.console.aliyun.com/#/v3/group/detail/cn-beijing/asg-2ze43yvwsqeze3z5hkrh/basicInfo",
    },
    {
      name: "KMS key",
      nativeType: "ACS::KMS::Key",
      nativeId: "625a28f6-2370-47a1-be39-81d2c1e85332",
      regionId: "cn-beijing",
      template:
        "https://kms.console.aliyun.com/{regionId}/key/detail/{nativeId}",
      expected:
        "https://kms.console.aliyun.com/cn-beijing/key/detail/625a28f6-2370-47a1-be39-81d2c1e85332",
    },
    {
      name: "Function Compute 2.0 service",
      nativeType: "ACS::FC::Service",
      nativeId: "ros-test",
      regionId: "cn-beijing",
      template:
        "https://fcnext.console.aliyun.com/{regionId}/services/{nativeId}/functions",
      expected:
        "https://fcnext.console.aliyun.com/cn-beijing/services/ros-test/functions",
    },
    {
      name: "PrivateLink endpoint",
      nativeType: "ACS::PrivateLink::VpcEndpoint",
      nativeId: "ep-2zei2766693500732a19",
      regionId: "cn-beijing",
      template:
        "https://vpc.console.aliyun.com/endpoint/{regionId}/endpoints/{nativeId}",
      expected:
        "https://vpc.console.aliyun.com/endpoint/cn-beijing/endpoints/ep-2zei2766693500732a19",
    },
    {
      name: "PrivateLink endpoint service",
      nativeType: "ACS::PrivateLink::VpcEndpointService",
      nativeId: "epsrv-2zess87mhcvhtza6surg",
      regionId: "cn-beijing",
      template:
        "https://vpc.console.aliyun.com/endpointservice/{regionId}/endpointservices/{nativeId}",
      expected:
        "https://vpc.console.aliyun.com/endpointservice/cn-beijing/endpointservices/epsrv-2zess87mhcvhtza6surg",
    },
    {
      name: "NAS file system",
      nativeType: "ACS::NAS::FileSystem",
      nativeId: "183864b5da",
      regionId: "cn-beijing",
      template:
        "https://nas.console.aliyun.com/{regionId}/filesystem/{nativeId}/info",
      expected:
        "https://nas.console.aliyun.com/cn-beijing/filesystem/183864b5da/info",
    },
    {
      name: "DataWorks workspace",
      nativeType: "ACS::DataWorks::Project",
      nativeId: "376",
      regionId: "eu-west-1",
      template:
        "https://dataworks.console.aliyun.com/workspace/detail?regionId={regionId}&workspaceId={nativeId}",
      expected:
        "https://dataworks.console.aliyun.com/workspace/detail?regionId=eu-west-1&workspaceId=376",
    },
    {
      name: "Cloud Backup vault",
      nativeType: "ACS::HBR::Vault",
      nativeId: "v-0001x27olhnk4k75oymq",
      template:
        "https://hbr.console.aliyun.com/#/vault?search=%7B%22VaultId%22:%22{nativeId}%22%7D",
      expected:
        "https://hbr.console.aliyun.com/#/vault?search=%7B%22VaultId%22:%22v-0001x27olhnk4k75oymq%22%7D",
    },
    {
      name: "ARMS trace application",
      nativeType: "ACS::ARMS::TraceApp",
      nativeId: "h9y3c809kq@430929d7db57273",
      regionId: "cn-beijing",
      template:
        "https://armsnext.console.aliyun.com/tracing#/tracing/{regionId}?appId={nativeId}",
      expected:
        "https://armsnext.console.aliyun.com/tracing#/tracing/cn-beijing?appId=h9y3c809kq%40430929d7db57273",
    },
    {
      name: "MaxCompute project",
      nativeType: "ACS::MaxCompute::Project",
      nativeId: "dfas",
      regionId: "cn-beijing",
      template:
        "https://maxcompute.console.aliyun.com/{regionId}/project-detail/{nativeId}/param-config",
      expected:
        "https://maxcompute.console.aliyun.com/cn-beijing/project-detail/dfas/param-config",
    },
    {
      name: "MNS queue",
      nativeType: "ACS::MessageService::Queue",
      nativeId: "TestSubForQueue",
      regionId: "cn-hangzhou",
      template:
        "https://mns.console.aliyun.com/region/{regionId}/queue/{nativeId}/detail",
      expected:
        "https://mns.console.aliyun.com/region/cn-hangzhou/queue/TestSubForQueue/detail",
    },
    {
      name: "MNS topic",
      nativeType: "ACS::MessageService::Topic",
      nativeId: "MNSTestConfig",
      regionId: "ap-southeast-1",
      template:
        "https://mns.console.aliyun.com/region/{regionId}/topic/{nativeId}/detail",
      expected:
        "https://mns.console.aliyun.com/region/ap-southeast-1/topic/MNSTestConfig/detail",
    },
    {
      name: "ActionTrail trail",
      nativeType: "ACS::ActionTrail::Trail",
      nativeId: "audit-trail-202504221403",
      regionId: "cn-hangzhou",
      template:
        "https://actiontrail.console.aliyun.com/{regionId}/trails/{nativeId}",
      expected:
        "https://actiontrail.console.aliyun.com/cn-hangzhou/trails/audit-trail-202504221403",
    },
    {
      name: "RocketMQ 5.0 instance",
      nativeType: "ACS::RocketMQ::Instance",
      nativeId: "rmq-cn-1x64n41r701",
      regionId: "cn-shenzhen",
      template:
        "https://ons.console.aliyun.com/region/{regionId}/instance/{nativeId}/detail",
      expected:
        "https://ons.console.aliyun.com/region/cn-shenzhen/instance/rmq-cn-1x64n41r701/detail",
    },
    {
      name: "ECS snapshot",
      nativeType: "ACS::ECS::Snapshot",
      nativeId: "s-uf6e50w6cz2srb6p62rq",
      regionId: "cn-shanghai",
      template:
        "https://ecs.console.aliyun.com/#/snapshotDetail/{regionId}/{nativeId}/detail",
      expected:
        "https://ecs.console.aliyun.com/#/snapshotDetail/cn-shanghai/s-uf6e50w6cz2srb6p62rq/detail",
    },
    {
      name: "Express Connect VBR",
      nativeType: "ACS::ExpressConnect::VirtualBorderRouter",
      nativeId: "vbr-uf6n1hcauwgkplhkrvbmj",
      regionId: "cn-shanghai",
      template:
        "https://expressconnect.console.aliyun.com/vbr/{regionId}/detail/{nativeId}",
      expected:
        "https://expressconnect.console.aliyun.com/vbr/cn-shanghai/detail/vbr-uf6n1hcauwgkplhkrvbmj",
    },
    {
      name: "Cloud-native API Gateway",
      nativeType: "ACS::APIG::Gateway",
      nativeId: "gw-d9a8qtem1hkg353fq5qg",
      regionId: "cn-hongkong",
      template:
        "https://apig.console.aliyun.com/#/{regionId}/gateway/{nativeId}/detail",
      expected:
        "https://apig.console.aliyun.com/#/cn-hongkong/gateway/gw-d9a8qtem1hkg353fq5qg/detail",
    },
    {
      name: "Container Registry instance",
      nativeType: "ACS::CR::Instance",
      nativeId: "cri-x3r6aif33t5om4ag",
      regionId: "cn-hongkong",
      template:
        "https://cr.console.aliyun.com/{regionId}/instance/{nativeId}/dashboard",
      expected:
        "https://cr.console.aliyun.com/cn-hongkong/instance/cri-x3r6aif33t5om4ag/dashboard",
    },
    {
      name: "DTS instance",
      nativeType: "ACS::DTS::Instance",
      nativeId: "dtsj06s5s8yg9ws54i",
      regionId: "cn-shenzhen",
      template:
        "https://dtsnew.console.aliyun.com/sbe/detail/basic-info/{nativeId}?regionId={regionId}",
      expected:
        "https://dtsnew.console.aliyun.com/sbe/detail/basic-info/dtsj06s5s8yg9ws54i?regionId=cn-shenzhen",
    },
    {
      name: "ECI container group",
      nativeType: "ACS::ECI::ContainerGroup",
      nativeId: "eci-bp1dqcx3r4vw5xdiwt6g",
      regionId: "cn-hangzhou",
      template:
        "https://eci.console.aliyun.com/#/eci/{regionId}/detail/{nativeId}/containers",
      expected:
        "https://eci.console.aliyun.com/#/eci/cn-hangzhou/detail/eci-bp1dqcx3r4vw5xdiwt6g/containers",
    },
    {
      name: "Tablestore instance",
      nativeType: "ACS::OTS::Instance",
      nativeId: "MyInstance-FFA",
      regionId: "cn-hangzhou",
      template: "https://otsnext.console.aliyun.com/{regionId}/{nativeId}/list",
      expected:
        "https://otsnext.console.aliyun.com/cn-hangzhou/MyInstance-FFA/list",
    },
    {
      name: "Simple Application Server instance",
      nativeType: "ACS::SWAS::Instance",
      nativeId: "06827a620a544e32acd395cd2e0c9e06",
      regionId: "cn-hangzhou",
      template:
        "https://swasnext.console.aliyun.com/servers/{regionId}/{nativeId}/dashboard",
      expected:
        "https://swasnext.console.aliyun.com/servers/cn-hangzhou/06827a620a544e32acd395cd2e0c9e06/dashboard",
    },
    {
      name: "PolarDB cluster",
      nativeType: "ACS::PolarDB::DBCluster",
      nativeId: "pc-bp15nw3ahgmo39usb",
      regionId: "cn-hangzhou",
      template:
        "https://polardb.console.aliyun.com/{regionId}/cluster/{nativeId}",
      expected:
        "https://polardb.console.aliyun.com/cn-hangzhou/cluster/pc-bp15nw3ahgmo39usb",
    },
    {
      name: "AnalyticDB PostgreSQL instance",
      nativeType: "ACS::GPDB::DBInstance",
      nativeId: "gp-2ze0wsw1kv4hak46h",
      regionId: "cn-beijing",
      template:
        "https://gpdbnext.console.aliyun.com/gpdb/{regionId}/list/nav/{nativeId}/StorageElastic/basic",
      expected:
        "https://gpdbnext.console.aliyun.com/gpdb/cn-beijing/list/nav/gp-2ze0wsw1kv4hak46h/StorageElastic/basic",
    },
    {
      name: "PolarDB-X 1.0 instance",
      nativeType: "ACS::DRDS::DBInstance",
      nativeId: "drdsbggax89077co",
      regionId: "cn-beijing",
      template:
        "https://drdsnew.console.aliyun.com/index#/{nativeId}/instDetail/drdsInstanceInfo",
      expected:
        "https://drdsnew.console.aliyun.com/index#/drdsbggax89077co/instDetail/drdsInstanceInfo",
    },
    {
      name: "EMR Serverless Spark workspace",
      nativeType: "ACS::EmrServerlessSpark::Workspace",
      nativeId: "w-1902a9dd3298cb73",
      regionId: "cn-beijing",
      template:
        "https://emr-next.console.aliyun.com/spark/#/region/{regionId}/workspace/{nativeId}/overview",
      expected:
        "https://emr-next.console.aliyun.com/spark/#/region/cn-beijing/workspace/w-1902a9dd3298cb73/overview",
    },
    {
      name: "SelectDB instance",
      nativeType: "ACS::SelectDB::DBInstance",
      nativeId: "selectdb-cn-hic46hvqk04",
      regionId: "cn-shanghai",
      template:
        "https://selectdb.console.aliyun.com/{regionId}/instanceDetail/{nativeId}/detail",
      expected:
        "https://selectdb.console.aliyun.com/cn-shanghai/instanceDetail/selectdb-cn-hic46hvqk04/detail",
    },
    {
      name: "RocketMQ 4.0 instance",
      nativeType: "ACS::Ons::Instance",
      nativeId: "MQ_INST_1754580903499898_BYIBHHgf",
      regionId: "cn-zhangjiakou",
      template:
        "https://ons.console.aliyun.com/region/{regionId}/instance/{nativeId}/detail",
      expected:
        "https://ons.console.aliyun.com/region/cn-zhangjiakou/instance/MQ_INST_1754580903499898_BYIBHHgf/detail",
    },
    {
      name: "MSE cloud-native gateway",
      nativeType: "ACS::MSE::Gateway",
      nativeId: "gw-ee30416e02ad48dd8a7d73a51438313a",
      regionId: "cn-beijing",
      template:
        "https://mse.console.aliyun.com/#/gateway/basicInfo?Id={nativeId}",
      expected:
        "https://mse.console.aliyun.com/#/gateway/basicInfo?Id=gw-ee30416e02ad48dd8a7d73a51438313a",
    },
    {
      name: "Graph Database instance",
      nativeType: "ACS::GraphDatabase::DbInstance",
      nativeId: "gds-bp19751d4if2o9q3",
      regionId: "cn-hangzhou",
      template:
        "https://gdb.console.aliyun.com/#/InstanceDetail?dbInstanceId={nativeId}&regionId={regionId}",
      expected:
        "https://gdb.console.aliyun.com/#/InstanceDetail?dbInstanceId=gds-bp19751d4if2o9q3&regionId=cn-hangzhou",
    },
    {
      name: "EBS disk replica group",
      nativeType: "ACS::EBS::DiskReplicaGroup",
      nativeId: "pg-QyPsBgMIAwEnl7uD",
      regionId: "cn-shenzhen",
      template:
        "https://ebs.console.aliyun.com/consistencyGroup/{regionId}/{nativeId}",
      expected:
        "https://ebs.console.aliyun.com/consistencyGroup/cn-shenzhen/pg-QyPsBgMIAwEnl7uD",
    },
    {
      name: "Lindorm instance",
      nativeType: "ACS::Lindorm::Instance",
      nativeId: "ld-wz9kvnooxaio304np",
      regionId: "cn-shenzhen",
      template:
        "https://lindorm.console.aliyun.com/{regionId}/cluster/lindorm/{nativeId}/info",
      expected:
        "https://lindorm.console.aliyun.com/cn-shenzhen/cluster/lindorm/ld-wz9kvnooxaio304np/info",
    },
    {
      name: "EBS disk replica pair",
      nativeType: "ACS::EBS::DiskReplicaPair",
      nativeId: "pair-cn-fhh41iduf003",
      regionId: "cn-beijing",
      template:
        "https://ebs.console.aliyun.com/dataProtection/{regionId}/{nativeId}",
      expected:
        "https://ebs.console.aliyun.com/dataProtection/cn-beijing/pair-cn-fhh41iduf003",
    },
    {
      name: "Intelligent Computing Lingjun cluster",
      nativeType: "ACS::Eflo::Cluster",
      nativeId: "i110585531750244587257",
      regionId: "cn-hangzhou",
      template:
        "https://lingjun.console.aliyun.com/{regionId}/edp-bms/hosted-cluster/cluster-details?ClusterId={nativeId}",
      expected:
        "https://lingjun.console.aliyun.com/cn-hangzhou/edp-bms/hosted-cluster/cluster-details?ClusterId=i110585531750244587257",
    },
    {
      name: "DataHub project",
      nativeType: "ACS::DataHub::Project",
      nativeId: "ros_ut_do_not_delete",
      regionId: "cn-beijing",
      template:
        "https://dhsnext.console.aliyun.com/{regionId}/projects/{nativeId}",
      expected:
        "https://dhsnext.console.aliyun.com/cn-beijing/projects/ros_ut_do_not_delete",
    },
    {
      name: "Cloud Storage Gateway",
      nativeType: "ACS::CloudStorageGateway::Gateway",
      nativeId: "gw-000i3gooyn4mhmtwqzk5",
      regionId: "cn-hangzhou",
      template: "https://sgwnew.console.aliyun.com/#/gatewayInfo/{nativeId}",
      expected:
        "https://sgwnew.console.aliyun.com/#/gatewayInfo/gw-000i3gooyn4mhmtwqzk5",
    },
    {
      name: "ClickHouse enterprise cluster",
      nativeType: "ACS::ClickHouse::EnterpriseDBCluster",
      nativeId: "cc-bp1x60eu54480u69b",
      regionId: "cn-hangzhou",
      template:
        "https://clickhouse.console.aliyun.com/clickhouse/{regionId}/list/enterprise/{nativeId}/basic",
      expected:
        "https://clickhouse.console.aliyun.com/clickhouse/cn-hangzhou/list/enterprise/cc-bp1x60eu54480u69b/basic",
    },
    {
      name: "Kafka instance",
      nativeType: "ACS::AliKafka::Instance",
      nativeId: "alikafka_post-cn-exo4thlao001",
      regionId: "cn-shenzhen",
      template:
        "https://kafka.console.aliyun.com/region/{regionId}/instance/{nativeId}/detail",
      expected:
        "https://kafka.console.aliyun.com/region/cn-shenzhen/instance/alikafka_post-cn-exo4thlao001/detail",
    },
    {
      name: "AnalyticDB MySQL lakehouse cluster",
      nativeType: "ACS::ADB::DBClusterLakeVersion",
      nativeId: "amv-bp12m6a004080o2k",
      regionId: "cn-hangzhou",
      template:
        "https://ads.console.aliyun.com/adb/{regionId}/instances/v5/{nativeId}/basic",
      expected:
        "https://ads.console.aliyun.com/adb/cn-hangzhou/instances/v5/amv-bp12m6a004080o2k/basic",
    },
    {
      name: "Alibaba Cloud DNS domain",
      nativeType: "ACS::Alidns::Domain",
      nativeId: "yingzhao-wty.top",
      template:
        "https://dnsnext.console.aliyun.com/authoritative/domains/{nativeId}",
      expected:
        "https://dnsnext.console.aliyun.com/authoritative/domains/yingzhao-wty.top",
    },
    {
      name: "Classic API Gateway instance dashboard",
      nativeType: "ACS::ApiGateway::Instance",
      nativeId: "apigateway-sz-a692bc76e5b8",
      regionId: "cn-shenzhen",
      template:
        "https://apigateway.console.aliyun.com/#/{regionId}/dashboard/view?instance={nativeId}",
      expected:
        "https://apigateway.console.aliyun.com/#/cn-shenzhen/dashboard/view?instance=apigateway-sz-a692bc76e5b8",
    },
    {
      name: "E-HPC cluster",
      nativeType: "ACS::Ehpc::Cluster",
      nativeId: "ehpc-bj-gbpakfrqhh",
      regionId: "cn-beijing",
      template:
        "https://ehpc.console.aliyun.com/#/clusterdetail/info/{nativeId}?referrer=%2Fcluster&regionId={regionId}",
      expected:
        "https://ehpc.console.aliyun.com/#/clusterdetail/info/ehpc-bj-gbpakfrqhh?referrer=%2Fcluster&regionId=cn-beijing",
    },
    {
      name: "Cloud SSO group with composite resource ID",
      nativeType: "ACS::CloudSSO::Group",
      nativeId: "d-00t06hkvwx73:g-00kiwl1ssxg5lq1z7j31",
      regionId: "cn-shanghai",
      template:
        "https://cloudsso.console.aliyun.com/{regionId}/groups/{nativeId|suffix::}/info",
      expected:
        "https://cloudsso.console.aliyun.com/cn-shanghai/groups/g-00kiwl1ssxg5lq1z7j31/info",
    },
    {
      name: "DataWorks resource group with composite resource ID",
      nativeType: "ACS::DataWorks::DwResourceGroup",
      nativeId: "Serverless_res_group_210724245979777_671671867101761",
      regionId: "cn-shanghai",
      template:
        "https://dataworks.console.aliyun.com/resource/detail?id={nativeId|suffix:_}&regionId={regionId}",
      expected:
        "https://dataworks.console.aliyun.com/resource/detail?id=671671867101761&regionId=cn-shanghai",
    },
    {
      name: "ECI image cache with container group ID prefix",
      nativeType: "ACS::ECI::ImageCache",
      nativeId: "imc-bp10bq6ss4iv2c6udnkk",
      regionId: "cn-hangzhou",
      template:
        "https://eci.console.aliyun.com/#/eci/{regionId}/image/{nativeId|replacePrefix:imc-,eci-}/{nativeId}/productevents",
      expected:
        "https://eci.console.aliyun.com/#/eci/cn-hangzhou/image/eci-bp10bq6ss4iv2c6udnkk/imc-bp10bq6ss4iv2c6udnkk/productevents",
    },
    {
      name: "BPStudio application",
      nativeType: "ACS::BPStudio::Application",
      nativeId: "01IMAISLWALO3B3M",
      template:
        "https://bpstudio.console.aliyun.com/bpStudio/topo?AppId={nativeId}",
      expected:
        "https://bpstudio.console.aliyun.com/bpStudio/topo?AppId=01IMAISLWALO3B3M",
    },
    {
      name: "CEN instance",
      nativeType: "ACS::CEN::CenInstance",
      nativeId: "cen-b46rksvi4fcjop46b6",
      template: "https://cen.console.aliyun.com/cen/detail/{nativeId}",
      expected:
        "https://cen.console.aliyun.com/cen/detail/cen-b46rksvi4fcjop46b6",
    },
    {
      name: "CEN bandwidth package",
      nativeType: "ACS::CEN::CenBandwidthPackage",
      nativeId: "cenbwp-8hfrdbedeom4q3sei4",
      template:
        "https://cen.console.aliyun.com/cen/bandwidth?CenBandwidthPackageId={nativeId}&IncludeReservationData=true&IsOrKey=true",
      expected:
        "https://cen.console.aliyun.com/cen/bandwidth?CenBandwidthPackageId=cenbwp-8hfrdbedeom4q3sei4&IncludeReservationData=true&IsOrKey=true",
    },
    {
      name: "CDN domain",
      nativeType: "ACS::CDN::Domain",
      nativeId: "aiforeverything.com.cn",
      template: "https://cdn.console.aliyun.com/domain/detail/{nativeId}",
      expected:
        "https://cdn.console.aliyun.com/domain/detail/aiforeverything.com.cn",
    },
  ])(
    "expands the verified $name detail route",
    ({
      nativeType,
      nativeId,
      regionId = "cn-hangzhou",
      template,
      expected,
    }) => {
      expect(
        cloudConsoleURL({
          provider: "alicloud",
          nativeType,
          nativeId,
          regionId,
          consoleLinkTemplate: template,
        }),
      ).toBe(expected);
    },
  );

  it("encodes values inserted into provider-spec templates", () => {
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::ECS::Instance",
        nativeId: "i-production/blue",
        regionId: "cn-hangzhou",
        consoleLinkTemplate:
          "https://ecs.console.aliyun.com/server/region/{regionId}?instanceId={nativeId}",
      }),
    ).toBe(
      "https://ecs.console.aliyun.com/server/region/cn-hangzhou?instanceId=i-production%2Fblue",
    );
  });

  it("supports product-console templates that do not yet have a detail route", () => {
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::RDS::DBInstance",
        nativeId: "rm-production",
        regionId: "cn-hangzhou",
        consoleLinkTemplate: "https://rds.console.aliyun.com/",
      }),
    ).toBe("https://rds.console.aliyun.com/");
  });

  it("rejects absent, incomplete, or unsafe templates", () => {
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::ROS::Stack",
        nativeId: "stack-a",
        regionId: "cn-hangzhou",
      }),
    ).toBeUndefined();
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::ROS::Stack",
        nativeId: "stack-a",
        consoleLinkTemplate:
          "https://ros.console.aliyun.com/{regionId}/stacks/{nativeId}",
      }),
    ).toBeUndefined();
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::ROS::Stack",
        nativeId: "stack-a",
        regionId: "cn-hangzhou",
        consoleLinkTemplate:
          "javascript:alert(1)?region={regionId}&id={nativeId}",
      }),
    ).toBeUndefined();
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::ROS::Stack",
        nativeId: "stack-a",
        regionId: "cn-hangzhou",
        consoleLinkTemplate:
          "https://example.com/{unsupported}/stacks/{nativeId}",
      }),
    ).toBeUndefined();
  });

  it("builds AWS native console links from ARNs and network IDs", () => {
    expect(
      cloudConsoleURL({
        provider: "aws",
        nativeType: "AWS::EC2::Instance",
        nativeId: "arn:aws:ec2:us-east-1:123456789012:instance/i-1234567890",
      }),
    ).toBe(
      "https://console.aws.amazon.com/ec2/home?region=us-east-1#InstanceDetails:instanceId=i-1234567890",
    );
    expect(
      cloudConsoleURL({
        provider: "aws",
        nativeType: "AWS::EC2::Subnet",
        nativeId: "subnet-123",
        regionId: "us-west-2",
      }),
    ).toBe(
      "https://console.aws.amazon.com/vpcconsole/home?region=us-west-2#SubnetDetails:subnetId=subnet-123",
    );
  });

  it("covers every explicitly cataloged AWS resource and Resource Explorer discoveries", () => {
    const missing = awsCatalog.resource_types
      .map((resource) => resource.native_type)
      .filter(
        (nativeType) =>
          !cloudConsoleURL({
            provider: "aws",
            nativeType,
            nativeId:
              "arn:aws:example:us-east-1:123456789012:resource/resource-a",
            regionId: "us-east-1",
          }),
      );
    expect(awsCatalog.resource_types.length).toBeGreaterThan(0);
    expect(missing).toEqual([]);

    expect(
      cloudConsoleURL({
        provider: "aws",
        nativeType: "AWS::DynamoDB::Table",
        nativeId:
          "arn:aws:dynamodb:us-east-1:123456789012:table/orders-production",
      }),
    ).toBe(
      "https://resource-explorer.console.aws.amazon.com/resource-explorer/home?region=us-east-1#/search?query=id%3Aarn%3Aaws%3Adynamodb%3Aus-east-1%3A123456789012%3Atable%2Forders-production",
    );
  });

  it("omits resources without identity", () => {
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::OSS::Bucket",
        nativeId: "",
        consoleLinkTemplate:
          "https://oss.console.aliyun.com/bucket/oss-{regionId}/{nativeId}/object",
      }),
    ).toBeUndefined();
  });

  it("expands the console link declared by every Alibaba Cloud resource spec", () => {
    const invalid = alicloudSpecs.flatMap((spec) => {
      const templateValues = Object.fromEntries(
        [...spec.template.matchAll(/\{([A-Za-z][A-Za-z0-9]*)/g)]
          .map((match) => match[1])
          .filter((key) => key !== "nativeId" && key !== "regionId")
          .map((key) => [key, "value"]),
      );
      const url = cloudConsoleURL({
        provider: "alicloud",
        nativeType: spec.nativeType,
        nativeId: "resource-a",
        regionId: "cn-hangzhou",
        consoleLinkTemplate: spec.template,
        templateValues,
      });
      return spec.nativeType && spec.template && url ? [] : [spec.name];
    });

    expect(alicloudSpecs).toHaveLength(159);
    expect(invalid).toEqual([]);
  });

  it("keeps confirmed detail routes in their provider specs", () => {
    const templates = new Map(
      alicloudSpecs.map((spec) => [spec.nativeType, spec.template]),
    );

    expect(templates.get("ACS::SLS::Project")).toBe(
      "https://sls.console.aliyun.com/lognext/project/{nativeId}/overview?slsRegion={regionId}",
    );
    expect(templates.get("ACS::VPC::GatewayEndpoint")).toBe(
      "https://vpc.console.aliyun.com/vpc/{regionId}/gateway-endpoint",
    );
    expect(templates.get("ACS::VPC::RouterInterface")).toBe(
      "https://expressconnect.console.aliyun.com/peerconnection/{regionId}/vpc2vpc?ConnectionType=vpc2vpc&ProductForm=expressconnect&RouterInterfaceId={nativeId}&TabKey=vbr2vpc",
    );
    expect(templates.get("ACS::RDS::DBInstance")).toBe(
      "https://rdsnext.console.aliyun.com/detail/{nativeId}/basicInfo?region={regionId}",
    );
    expect(templates.get("ACS::Redis::DBInstance")).toBe(
      "https://kvstore.console.aliyun.com/Redis/instance/{regionId}/{nativeId}",
    );
    expect(templates.get("ACS::MongoDB::DBInstance")).toBe(
      "https://mongodb.console.aliyun.com/replicate/{regionId}/instances/{nativeId}/basicInfo",
    );
    expect(templates.get("ACS::Elasticsearch::Instance")).toBe(
      "https://elasticsearch.console.aliyun.com/{regionId}/instances/{nativeId}/base",
    );
    expect(templates.get("ACS::Elasticsearch::Logstash")).toBe(
      "https://elasticsearch.console.aliyun.com/{regionId}/logstashes/{nativeId}/base",
    );
    expect(templates.get("ACS::ROS::StackGroup")).toBe(
      "https://ros.console.aliyun.com/{regionId}/stackGroup/{nativeId}",
    );
    expect(templates.get("ACS::ESS::ScalingGroup")).toBe(
      "https://ess.console.aliyun.com/#/v3/group/detail/{regionId}/{nativeId}/basicInfo",
    );
    expect(templates.get("ACS::KMS::Key")).toBe(
      "https://kms.console.aliyun.com/{regionId}/key/detail/{nativeId}",
    );
    expect(templates.get("ACS::FC::Service")).toBe(
      "https://fcnext.console.aliyun.com/{regionId}/services/{nativeId}/functions",
    );
    expect(templates.get("ACS::PrivateLink::VpcEndpoint")).toBe(
      "https://vpc.console.aliyun.com/endpoint/{regionId}/endpoints/{nativeId}",
    );
    expect(templates.get("ACS::PrivateLink::VpcEndpointService")).toBe(
      "https://vpc.console.aliyun.com/endpointservice/{regionId}/endpointservices/{nativeId}",
    );
    expect(templates.get("ACS::NAS::FileSystem")).toBe(
      "https://nas.console.aliyun.com/{regionId}/filesystem/{nativeId}/info",
    );
    expect(templates.get("ACS::DataWorks::Project")).toBe(
      "https://dataworks.console.aliyun.com/workspace/detail?regionId={regionId}&workspaceId={nativeId}",
    );
    expect(templates.get("ACS::HBR::Vault")).toBe(
      "https://hbr.console.aliyun.com/#/vault?search=%7B%22VaultId%22:%22{nativeId}%22%7D",
    );
    expect(templates.get("ACS::ARMS::TraceApp")).toBe(
      "https://armsnext.console.aliyun.com/tracing#/tracing/{regionId}?appId={nativeId}",
    );
    expect(templates.get("ACS::MaxCompute::Project")).toBe(
      "https://maxcompute.console.aliyun.com/{regionId}/project-detail/{nativeId}/param-config",
    );
    expect(templates.get("ACS::MessageService::Queue")).toBe(
      "https://mns.console.aliyun.com/region/{regionId}/queue/{nativeId}/detail",
    );
    expect(templates.get("ACS::MessageService::Topic")).toBe(
      "https://mns.console.aliyun.com/region/{regionId}/topic/{nativeId}/detail",
    );
    expect(templates.get("ACS::ActionTrail::Trail")).toBe(
      "https://actiontrail.console.aliyun.com/{regionId}/trails/{nativeId}",
    );
    expect(templates.get("ACS::RocketMQ::Instance")).toBe(
      "https://ons.console.aliyun.com/region/{regionId}/instance/{nativeId}/detail",
    );
    expect(templates.get("ACS::ECS::Snapshot")).toBe(
      "https://ecs.console.aliyun.com/#/snapshotDetail/{regionId}/{nativeId}/detail",
    );
    expect(templates.get("ACS::ExpressConnect::VirtualBorderRouter")).toBe(
      "https://expressconnect.console.aliyun.com/vbr/{regionId}/detail/{nativeId}",
    );
    expect(templates.get("ACS::APIG::Gateway")).toBe(
      "https://apig.console.aliyun.com/#/{regionId}/gateway/{nativeId}/detail",
    );
    expect(templates.get("ACS::CR::Instance")).toBe(
      "https://cr.console.aliyun.com/{regionId}/instance/{nativeId}/dashboard",
    );
    expect(templates.get("ACS::DTS::Instance")).toBe(
      "https://dtsnew.console.aliyun.com/sbe/detail/basic-info/{nativeId}?regionId={regionId}",
    );
    expect(templates.get("ACS::ECI::ContainerGroup")).toBe(
      "https://eci.console.aliyun.com/#/eci/{regionId}/detail/{nativeId}/containers",
    );
    expect(templates.get("ACS::OTS::Instance")).toBe(
      "https://otsnext.console.aliyun.com/{regionId}/{nativeId}/list",
    );
    expect(templates.get("ACS::SWAS::Instance")).toBe(
      "https://swasnext.console.aliyun.com/servers/{regionId}/{nativeId}/dashboard",
    );
    expect(templates.get("ACS::PolarDB::DBCluster")).toBe(
      "https://polardb.console.aliyun.com/{regionId}/cluster/{nativeId}",
    );
    expect(templates.get("ACS::GPDB::DBInstance")).toBe(
      "https://gpdbnext.console.aliyun.com/gpdb/{regionId}/list/nav/{nativeId}/StorageElastic/basic",
    );
    expect(templates.get("ACS::DRDS::DBInstance")).toBe(
      "https://drdsnew.console.aliyun.com/index#/{nativeId}/instDetail/drdsInstanceInfo",
    );
    expect(templates.get("ACS::EmrServerlessSpark::Workspace")).toBe(
      "https://emr-next.console.aliyun.com/spark/#/region/{regionId}/workspace/{nativeId}/overview",
    );
    expect(templates.get("ACS::SelectDB::DBInstance")).toBe(
      "https://selectdb.console.aliyun.com/{regionId}/instanceDetail/{nativeId}/detail",
    );
    expect(templates.get("ACS::Ons::Instance")).toBe(
      "https://ons.console.aliyun.com/region/{regionId}/instance/{nativeId}/detail",
    );
    expect(templates.get("ACS::MSE::Gateway")).toBe(
      "https://mse.console.aliyun.com/#/gateway/basicInfo?Id={nativeId}",
    );
    expect(templates.get("ACS::GraphDatabase::DbInstance")).toBe(
      "https://gdb.console.aliyun.com/#/InstanceDetail?dbInstanceId={nativeId}&regionId={regionId}",
    );
    expect(templates.get("ACS::EBS::DiskReplicaGroup")).toBe(
      "https://ebs.console.aliyun.com/consistencyGroup/{regionId}/{nativeId}",
    );
    expect(templates.get("ACS::Lindorm::Instance")).toBe(
      "https://lindorm.console.aliyun.com/{regionId}/cluster/lindorm/{nativeId}/info",
    );
    expect(templates.get("ACS::EBS::DiskReplicaPair")).toBe(
      "https://ebs.console.aliyun.com/dataProtection/{regionId}/{nativeId}",
    );
    expect(templates.get("ACS::Eflo::Cluster")).toBe(
      "https://lingjun.console.aliyun.com/{regionId}/edp-bms/hosted-cluster/cluster-details?ClusterId={nativeId}",
    );
    expect(templates.get("ACS::DataHub::Project")).toBe(
      "https://dhsnext.console.aliyun.com/{regionId}/projects/{nativeId}",
    );
    expect(templates.get("ACS::CloudStorageGateway::Gateway")).toBe(
      "https://sgwnew.console.aliyun.com/#/gatewayInfo/{nativeId}",
    );
    expect(templates.get("ACS::ClickHouse::EnterpriseDBCluster")).toBe(
      "https://clickhouse.console.aliyun.com/clickhouse/{regionId}/list/enterprise/{nativeId}/basic",
    );
    expect(templates.get("ACS::AliKafka::Instance")).toBe(
      "https://kafka.console.aliyun.com/region/{regionId}/instance/{nativeId}/detail",
    );
    expect(templates.get("ACS::ADB::DBClusterLakeVersion")).toBe(
      "https://ads.console.aliyun.com/adb/{regionId}/instances/v5/{nativeId}/basic",
    );
    expect(templates.get("ACS::Alidns::Domain")).toBe(
      "https://dnsnext.console.aliyun.com/authoritative/domains/{nativeId}",
    );
    expect(templates.get("ACS::ApiGateway::Instance")).toBe(
      "https://apigateway.console.aliyun.com/#/{regionId}/dashboard/view?instance={nativeId}",
    );
    expect(templates.get("ACS::Ehpc::Cluster")).toBe(
      "https://ehpc.console.aliyun.com/#/clusterdetail/info/{nativeId}?referrer=%2Fcluster&regionId={regionId}",
    );
    expect(templates.get("ACS::CloudSSO::Group")).toBe(
      "https://cloudsso.console.aliyun.com/{regionId}/groups/{nativeId|suffix::}/info",
    );
    expect(templates.get("ACS::DataWorks::DwResourceGroup")).toBe(
      "https://dataworks.console.aliyun.com/resource/detail?id={nativeId|suffix:_}&regionId={regionId}",
    );
    expect(templates.get("ACS::ECI::ImageCache")).toBe(
      "https://eci.console.aliyun.com/#/eci/{regionId}/image/{nativeId|replacePrefix:imc-,eci-}/{nativeId}/productevents",
    );
    expect(templates.get("ACS::BPStudio::Application")).toBe(
      "https://bpstudio.console.aliyun.com/bpStudio/topo?AppId={nativeId}",
    );
    expect(templates.get("ACS::CEN::CenInstance")).toBe(
      "https://cen.console.aliyun.com/cen/detail/{nativeId}",
    );
    expect(templates.get("ACS::CEN::CenBandwidthPackage")).toBe(
      "https://cen.console.aliyun.com/cen/bandwidth?CenBandwidthPackageId={nativeId}&IncludeReservationData=true&IsOrKey=true",
    );
    expect(templates.get("ACS::CDN::Domain")).toBe(
      "https://cdn.console.aliyun.com/domain/detail/{nativeId}",
    );
    expect(templates.get("ACS::CEN::TransitRouter")).toBe(
      "https://cen.console.aliyun.com/cen/attachment/{regionId}/{parentId}/{nativeId}",
    );
    expect(templates.get("ACS::CEN::TransitRouterVpcAttachment")).toBe(
      "https://cen.console.aliyun.com/cen/attachment/{regionId}/{cenId}/{transitRouterId}",
    );
    expect(templates.get("ACS::CEN::CenRouteMap")).toBe(
      "https://cen.console.aliyun.com/cen/attachment/{regionId}/{cenId}/{transitRouterId}/routeTable?CenId={cenId}&TransitRouterId={transitRouterId}&TransitRouterRouteTableId={transitRouterRouteTableId}",
    );
    expect(templates.get("ACS::CEN::TransitRouterRouteTable")).toBe(
      "https://cen.console.aliyun.com/cen/attachment/{regionId}/{cenId}/{transitRouterId}/routeTable?CenId={cenId}&TransitRouterId={transitRouterId}&TransitRouterRouteTableId={transitRouterRouteTableId}",
    );
    expect(templates.get("ACS::CEN::TransitRouterPeerAttachment")).toBe(
      "https://cen.console.aliyun.com/cen/attachment/{regionId}/{cenId}/{transitRouterId}/crossInteregional?CenId={cenId}&TransitRouterId={transitRouterId}",
    );
    expect(templates.get("ACS::ARMS::Prometheus")).toBe(
      "https://cms.console.aliyun.com/prom/instances/details?clusterId={nativeId}&regionId={regionId}",
    );
    expect(templates.get("ACS::VPN::CustomerGateway")).toBe(
      "https://vpc.console.aliyun.com/vpn/{regionId}/vpn-clients/{nativeId}?CustomerGatewayId={nativeId}",
    );
    expect(templates.get("ACS::ECS::Disk")).toBe(
      "https://ecs.console.aliyun.com/diskdetail/{nativeId}/detail?regionId={regionId}",
    );
    expect(templates.get("ACS::PrivateZone::Zone")).toBe(
      "https://dnsnext.console.aliyun.com/privateDNS/zones/{nativeId}/records",
    );
    expect(templates.get("ACS::MSE::Cluster")).toBe(
      "https://mse.console.aliyun.com/#/InstanceList?region={regionId}",
    );
    expect(templates.get("ACS::SDDP::Instance")).toBe(
      "https://yundun.console.aliyun.com/?p=sddp#/sddp/assetCenter/manager/{regionId}",
    );
    expect(templates.get("ACS::DdosCoo::Instance")).toBe(
      "https://yundun.console.aliyun.com/?p=ddoscoo#/instance/{regionId}",
    );
    expect(templates.get("ACS::DBS::BackupPlan")).toBe(
      "https://dms.aliyun.com/#to=dbs_data_source",
    );
    expect(templates.get("ACS::PAI::Service")).toBe(
      "https://pai.console.aliyun.com/?regionId={regionId}#/sw?path=/eas",
    );
  });

  it("expands a spec-declared normalized field for a nested resource route", () => {
    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::CEN::TransitRouter",
        nativeId: "tr-bp1fgdi7zdack1uu0o1u8",
        regionId: "cn-hangzhou",
        consoleLinkTemplate:
          "https://cen.console.aliyun.com/cen/attachment/{regionId}/{parentId}/{nativeId}",
        templateValues: { parentId: "cen-b46rksvi4fcjop46b6" },
      }),
    ).toBe(
      "https://cen.console.aliyun.com/cen/attachment/cn-hangzhou/cen-b46rksvi4fcjop46b6/tr-bp1fgdi7zdack1uu0o1u8",
    );

    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::CEN::TransitRouter",
        nativeId: "tr-j6ca472kucep5zasasdfz",
        consoleLinkTemplate:
          "https://cen.console.aliyun.com/cen/attachment/{regionId}/{parentId}/{nativeId}",
        templateValues: {
          regionId: "cn-hongkong",
          parentId: "cen-b46rksvi4fcjop46b6",
        },
      }),
    ).toBe(
      "https://cen.console.aliyun.com/cen/attachment/cn-hongkong/cen-b46rksvi4fcjop46b6/tr-j6ca472kucep5zasasdfz",
    );

    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::CEN::TransitRouter",
        nativeId: "tr-bp1fgdi7zdack1uu0o1u8",
        regionId: "cn-hangzhou",
        consoleLinkTemplate:
          "https://cen.console.aliyun.com/cen/attachment/{regionId}/{parentId}/{nativeId}",
      }),
    ).toBeUndefined();

    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::CEN::TransitRouterVpcAttachment",
        nativeId: "tr-attach-b9owgteq5t2u1izyqx",
        regionId: "ap-northeast-2",
        consoleLinkTemplate:
          "https://cen.console.aliyun.com/cen/attachment/{regionId}/{cenId}/{transitRouterId}",
        templateValues: {
          cenId: "cen-b46rksvi4fcjop46b6",
          transitRouterId: "tr-mj7f5z1btac2s1x4jusp5",
        },
      }),
    ).toBe(
      "https://cen.console.aliyun.com/cen/attachment/ap-northeast-2/cen-b46rksvi4fcjop46b6/tr-mj7f5z1btac2s1x4jusp5",
    );

    const routeTableTemplate =
      "https://cen.console.aliyun.com/cen/attachment/{regionId}/{cenId}/{transitRouterId}/routeTable?CenId={cenId}&TransitRouterId={transitRouterId}&TransitRouterRouteTableId={transitRouterRouteTableId}";
    for (const nativeType of [
      "ACS::CEN::CenRouteMap",
      "ACS::CEN::TransitRouterRouteTable",
    ]) {
      expect(
        cloudConsoleURL({
          provider: "alicloud",
          nativeType,
          nativeId:
            nativeType === "ACS::CEN::CenRouteMap"
              ? "cenrmap-eple19ff8example"
              : "vtb-hp312jq8dl5osnn2ee49u",
          regionId: "cn-huhehaote",
          consoleLinkTemplate: routeTableTemplate,
          templateValues: {
            cenId: "cen-b46rksvi4fcjop46b6",
            transitRouterId: "tr-hp3wvvn3ycxzmzv11sx4p",
            transitRouterRouteTableId: "vtb-hp312jq8dl5osnn2ee49u",
          },
        }),
      ).toBe(
        "https://cen.console.aliyun.com/cen/attachment/cn-huhehaote/cen-b46rksvi4fcjop46b6/tr-hp3wvvn3ycxzmzv11sx4p/routeTable?CenId=cen-b46rksvi4fcjop46b6&TransitRouterId=tr-hp3wvvn3ycxzmzv11sx4p&TransitRouterRouteTableId=vtb-hp312jq8dl5osnn2ee49u",
      );
    }

    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::CEN::TransitRouterPeerAttachment",
        nativeId: "tr-attach-n02ubayiexample",
        regionId: "cn-huhehaote",
        consoleLinkTemplate:
          "https://cen.console.aliyun.com/cen/attachment/{regionId}/{cenId}/{transitRouterId}/crossInteregional?CenId={cenId}&TransitRouterId={transitRouterId}",
        templateValues: {
          cenId: "cen-b46rksvi4fcjop46b6",
          transitRouterId: "tr-hp3wvvn3ycxzmzv11sx4p",
        },
      }),
    ).toBe(
      "https://cen.console.aliyun.com/cen/attachment/cn-huhehaote/cen-b46rksvi4fcjop46b6/tr-hp3wvvn3ycxzmzv11sx4p/crossInteregional?CenId=cen-b46rksvi4fcjop46b6&TransitRouterId=tr-hp3wvvn3ycxzmzv11sx4p",
    );

    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::ARMS::Prometheus",
        nativeId: "77d9b5cc04cbccd4",
        regionId: "cn-huhehaote",
        consoleLinkTemplate:
          "https://arms.console.aliyun.com/#/promDetail/{regionId}/{nativeId}/market",
      }),
    ).toBe(
      "https://arms.console.aliyun.com/#/promDetail/cn-huhehaote/77d9b5cc04cbccd4/market",
    );

    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::VPN::CustomerGateway",
        nativeId: "cgw-hp3lkxnlxhnh3paht6e7p",
        regionId: "cn-huhehaote",
        consoleLinkTemplate:
          "https://vpc.console.aliyun.com/vpn/{regionId}/vpn-clients/{nativeId}?CustomerGatewayId={nativeId}",
      }),
    ).toBe(
      "https://vpc.console.aliyun.com/vpn/cn-huhehaote/vpn-clients/cgw-hp3lkxnlxhnh3paht6e7p?CustomerGatewayId=cgw-hp3lkxnlxhnh3paht6e7p",
    );

    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::ECS::Disk",
        nativeId: "d-6we5ypd9as5am03yq0pv",
        regionId: "ap-northeast-1",
        consoleLinkTemplate:
          "https://ecs.console.aliyun.com/diskdetail/{nativeId}/detail?regionId={regionId}",
      }),
    ).toBe(
      "https://ecs.console.aliyun.com/diskdetail/d-6we5ypd9as5am03yq0pv/detail?regionId=ap-northeast-1",
    );

    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::PrivateZone::Zone",
        nativeId: "f8a5ccb7f4157ccc3a630ac33be8d658",
        consoleLinkTemplate:
          "https://dnsnext.console.aliyun.com/privateDNS/zones/{nativeId}/records",
      }),
    ).toBe(
      "https://dnsnext.console.aliyun.com/privateDNS/zones/f8a5ccb7f4157ccc3a630ac33be8d658/records",
    );

    expect(
      cloudConsoleURL({
        provider: "alicloud",
        nativeType: "ACS::DdosCoo::Instance",
        nativeId: "ddoscoo-instance-a",
        regionId: "cn-hangzhou",
        consoleLinkTemplate:
          "https://yundun.console.aliyun.com/?p=ddoscoo#/instance/{regionId}",
      }),
    ).toBe(
      "https://yundun.console.aliyun.com/?p=ddoscoo#/instance/cn-hangzhou",
    );
  });
});
