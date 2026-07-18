import { afterEach, expect, it, vi } from "vitest";
import type {
  AccountTopologyView,
  RegionTopologyView,
  ResourceGraphTopologyView,
  TopologyResponse,
  TopologyView,
  VPCTopologyView,
} from "./types";
import {
  APIRequestError,
  continueCleanupExecution,
  createConnection,
  createExecution,
  findAssets,
  findAssetsByNativeIDs,
  getCleanupTaskLogs,
  getAliCloudOAuthFlow,
  getJob,
  getTopology,
  listAssets,
  listConnectionRegions,
  listConnections,
  listProviderCatalog,
  listScans,
  startAliCloudOAuthFlow,
} from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
});

it("sends cleanup log filters to the backend", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ items: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);

  await getCleanupTaskLogs("connection-a", "cleanup/a", "cursor-a", {
    resourceID: "i-production",
    resourceKindIDs: ["kind-stack", "kind-instance", "kind-stack"],
  });

  expect(fetchMock.mock.calls[0]?.[0]).toBe(
    "/api/cleanup/cleanup%2Fa/logs?resource_id=i-production&resource_kind_id=kind-stack&resource_kind_id=kind-instance&limit=100&before=cursor-a&connection_id=connection-a",
  );
});

it("posts cleanup execution concurrency with confirmation", async () => {
  const created = {
    id: "execution-a",
    connection_id: "connection-a",
    cleanup_task_id: "cleanup/a",
    status: "pending",
    concurrency: 35,
    requested_by: "operator",
    idempotency_key: "execute-key",
    created_at: "2026-08-10T00:00:00Z",
  };
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify(created), {
      status: 202,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);

  await expect(
    createExecution("connection-a", "cleanup/a", "execute-key", 35, {
      acknowledged: true,
    }),
  ).resolves.toEqual(created);

  expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
    method: "POST",
    body: JSON.stringify({
      idempotency_key: "execute-key",
      concurrency: 35,
      confirmation: { acknowledged: true },
    }),
  });
});

it("posts a failed cleanup continuation with an idempotency key", async () => {
  const continued = {
    id: "execution-a",
    connection_id: "connection-a",
    cleanup_task_id: "cleanup/a",
    status: "running",
    requested_by: "operator",
    idempotency_key: "execute-key",
    continue_idempotency_key: "continue-key",
    continue_count: 1,
    created_at: "2026-08-04T00:00:00Z",
  };
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify(continued), {
      status: 202,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);

  await expect(
    continueCleanupExecution("connection-a", "cleanup/a", "continue-key", 20),
  ).resolves.toEqual(continued);

  expect(fetchMock.mock.calls[0]?.[0]).toBe(
    "/api/cleanup/cleanup%2Fa/continue?connection_id=connection-a",
  );
  expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
    method: "POST",
    body: JSON.stringify({ idempotency_key: "continue-key", concurrency: 20 }),
  });
});

it("encodes the typed topology query without legacy traversal fields", async () => {
  const response: TopologyResponse = {
    revision: {
      inventory: "inventory-a",
      graph: "graph-a",
      spec_bundle: "spec-a",
      projected_at: "2026-07-24T00:00:00Z",
    },
    coverage: { status: "complete", failed_shards: 0 },
    view: { kind: "account", regions: [] },
    truncated: false,
  };
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify(response), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);

  await getTopology("connection-a", {
    focus_key: "region:Y24taGFuZ3pob3U",
    cursor: "cursor-2",
    limit: 200,
    resource_class: "compute.instance",
    resource_kind_id: ["ack-cluster", "ecs-instance"],
    risk: "findings",
  });

  const path = String(fetchMock.mock.calls[0]?.[0]);
  expect(path).toContain("/api/topology?");
  expect(path).toContain("connection_id=connection-a");
  expect(path).toContain("focus_key=region%3AY24taGFuZ3pob3U");
  expect(path).toContain("cursor=cursor-2");
  expect(path).toContain("limit=200");
  expect(path).toContain("resource_class=compute.instance");
  expect(path).toContain("resource_kind_id=ack-cluster");
  expect(path).toContain("resource_kind_id=ecs-instance");
  expect(path.match(/resource_kind_id=/g)).toHaveLength(2);
  expect(path).toContain("risk=findings");
  expect(path).not.toContain("parent_key");
  expect(path).not.toContain("depth");
  expect(path).not.toContain("node_limit");
});

it("types every discriminated topology view returned by the API", () => {
  const account: AccountTopologyView = { kind: "account", regions: [] };
  const region: RegionTopologyView = {
    kind: "region",
    region: { key: "region-a", name: "杭州", native_id: "cn-hangzhou" },
    public_resources: {
      key: "region-public-a",
      name: "地域公共资源",
      resource_count: 0,
      cleanup: { selectable: false, potential_blockers: 0 },
    },
    vpcs: [],
  };
  const resourceGraph: ResourceGraphTopologyView = {
    kind: "resource_graph",
    context: { key: "global", name: "账号全局资源" },
    resources: [],
    edges: [],
  };
  const vpc: VPCTopologyView = {
    kind: "vpc",
    region: { key: "region-a", name: "杭州", native_id: "cn-hangzhou" },
    vpc: { key: "vpc-a", name: "生产网络", native_id: "vpc-a" },
    public_resource_keys: [],
    vswitches: [],
    resources: [],
    edges: [],
  };
  const views: TopologyView[] = [account, region, resourceGraph, vpc];

  expect(views.map((view) => view.kind)).toEqual([
    "account",
    "region",
    "resource_graph",
    "vpc",
  ]);
});

it("preserves the stable topology cursor-stale error code", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        error: {
          code: "topology.cursor_stale",
          message: "topology cursor belongs to an older revision",
        },
      }),
      {
        status: 409,
        headers: { "Content-Type": "application/json" },
      },
    ),
  );
  vi.stubGlobal("fetch", fetchMock);

  await expect(
    getTopology("connection-a", {
      focus_key: "account-global",
      cursor: "cursor-2",
      limit: 200,
    }),
  ).rejects.toMatchObject({
    name: "APIRequestError",
    code: "topology.cursor_stale",
  } satisfies Partial<APIRequestError>);
});

it("encodes resource filters before cursor pagination", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ items: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);

  await listAssets("connection-a", {
    cursor: "cursor-2",
    limit: 50,
    query: "vpc prod",
    provider: "alicloud",
    capability: "cleanup",
    resourceKindIDs: ["alicloud:ACS::VPC::VPC", "alicloud:ACS::ECS::Instance"],
    assetIDs: ["asset-a", "asset-b"],
    nativeIDs: ["vpc-a", "vsw-a"],
  });

  expect(fetchMock).toHaveBeenCalledWith(
    "/api/assets?limit=50&cursor=cursor-2&q=vpc+prod&provider=alicloud&capability=cleanup&resource_kind_id=alicloud%3AACS%3A%3AVPC%3A%3AVPC&resource_kind_id=alicloud%3AACS%3A%3AECS%3A%3AInstance&asset_id=asset-a&asset_id=asset-b&native_id=vpc-a&native_id=vsw-a&connection_id=connection-a",
    expect.any(Object),
  );
});

it("passes the scan-list abort signal to fetch", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ items: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const controller = new AbortController();

  await listScans("connection-a", "cursor-2", 50, controller.signal);

  expect(fetchMock).toHaveBeenCalledWith(
    "/api/scans?limit=50&cursor=cursor-2&connection_id=connection-a",
    expect.objectContaining({ signal: controller.signal }),
  );
});

it("includes closed scanned resources when resolving property references", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ items: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);

  await findAssetsByNativeIDs("connection-a", ["vpc-a", "vsw-a"]);

  expect(fetchMock).toHaveBeenCalledWith(
    "/api/assets?limit=100&native_id=vpc-a&native_id=vsw-a&include_closed=true&connection_id=connection-a",
    expect.any(Object),
  );
});

it("resolves stable asset IDs with one targeted request including closed resources", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        items: [
          {
            id: "asset-closed",
            identity: {
              provider: "alicloud",
              partition: "public",
              connection_id: "connection-a",
              native_type: "ACS::ECS::Instance",
              native_id: "i-closed",
            },
          },
        ],
      }),
      {
        status: 200,
        headers: { "Content-Type": "application/json" },
      },
    ),
  );
  vi.stubGlobal("fetch", fetchMock);

  const assets = await findAssets("connection-a", [
    "asset-closed",
    "asset-missing",
  ]);

  expect(assets.map((asset) => asset.id)).toEqual(["asset-closed"]);
  expect(fetchMock).toHaveBeenCalledWith(
    "/api/assets?limit=50&asset_id=asset-closed&asset_id=asset-missing&include_closed=true&connection_id=connection-a",
    expect.any(Object),
  );
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

it("encodes the current panorama canvas for server-side asset search", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ items: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);

  await listAssets("connection-a", {
    limit: 50,
    query: "ros-test-beijing",
    canvas: "vpc",
    regionID: "cn-beijing",
    vpcID: "vpc-production",
    searchOrder: "panorama",
  });

  expect(fetchMock).toHaveBeenCalledWith(
    "/api/assets?limit=50&q=ros-test-beijing&canvas=vpc&region_id=cn-beijing&vpc_id=vpc-production&order=panorama-search&connection_id=connection-a",
    expect.any(Object),
  );
});

it("encodes provider filtering for connection pages", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ items: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);

  await listConnections({ limit: 50, provider: "aws" });

  expect(fetchMock).toHaveBeenCalledWith(
    "/api/connections?limit=50&provider=aws",
    expect.any(Object),
  );
});

it("loads all connection regions without pagination parameters and scopes job status", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ items: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ id: "job-a", status: "running" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  vi.stubGlobal("fetch", fetchMock);

  await listConnectionRegions("connection-a", {
    lifecycle: "active",
    query: "杭州",
  });
  await getJob("connection-a", "job-a");

  expect(fetchMock).toHaveBeenNthCalledWith(
    1,
    "/api/connections/connection-a/regions?lifecycle=active&q=%E6%9D%AD%E5%B7%9E",
    expect.any(Object),
  );
  expect(fetchMock).toHaveBeenNthCalledWith(
    2,
    "/api/jobs/job-a?connection_id=connection-a",
    expect.any(Object),
  );
});

it("sends the connection site at the top level without duplicating it in credential values", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        id: "connection-a",
        name: "international",
        provider: "alicloud",
        site: "intl",
      }),
      {
        status: 201,
        headers: { "Content-Type": "application/json" },
      },
    ),
  );
  vi.stubGlobal("fetch", fetchMock);

  await createConnection({
    name: "international",
    provider: "alicloud",
    site: "intl",
    credential: {
      type: "access_key",
      values: { access_key_id: "id", access_key_secret: "secret" },
    },
  });

  const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
  expect(JSON.parse(String(init.body))).toEqual({
    name: "international",
    provider: "alicloud",
    site: "intl",
    credential: {
      type: "access_key",
      values: { access_key_id: "id", access_key_secret: "secret" },
    },
  });
  expect(JSON.parse(String(init.body)).credential.values).not.toHaveProperty(
    "site",
  );
});

it("starts and polls an Alibaba Cloud OAuth flow without client-side credential material", async () => {
  const pending = {
    id: "oauth-flow/a",
    status: "pending",
    authorization_url:
      "https://signin.alibabacloud.com/oauth2/v1/auth?state=opaque",
    expires_at: "2026-07-27T12:05:00Z",
  };
  const authorized = {
    id: "oauth-flow/a",
    status: "authorized",
    expires_at: "2026-07-27T12:05:00Z",
  };
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(JSON.stringify(pending), {
        status: 201,
        headers: { "Content-Type": "application/json" },
      }),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify(authorized), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  vi.stubGlobal("fetch", fetchMock);

  await expect(startAliCloudOAuthFlow("intl")).resolves.toEqual(pending);
  await expect(getAliCloudOAuthFlow("oauth-flow/a")).resolves.toEqual(
    authorized,
  );

  expect(fetchMock.mock.calls[0]?.[0]).toBe(
    "/api/providers/alicloud/oauth/flows",
  );
  expect(
    JSON.parse(String((fetchMock.mock.calls[0]?.[1] as RequestInit).body)),
  ).toEqual({ site: "intl" });
  expect(fetchMock.mock.calls[1]?.[0]).toBe(
    "/api/providers/alicloud/oauth/flows/oauth-flow%2Fa",
  );
  expect(JSON.stringify([pending, authorized])).not.toMatch(
    /access_token|refresh_token|access_key|security_token/i,
  );
});

it("maps runtime catalog labels onto compatible compiled specs", async () => {
  const compiledKind = {
    id: "alicloud:ACS::ECS::Instance",
    provider: "alicloud",
    native_type: "ACS::ECS::Instance",
    capabilities: ["indexed"],
    bundle_revision: "compiled-a",
  };
  const runtimeKind = {
    ...compiledKind,
    bundle_revision: "runtime-a",
    field_display_names: {
      accountId: {
        "zh-CN": "账号 ID",
        "en-US": "Account ID",
      },
    },
  };
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify([
          {
            provider: "alicloud",
            revision: "compiled-a",
            hash: "hash-a",
            kinds_revision: "runtime-a",
            kinds: [
              runtimeKind,
              {
                id: "alicloud:ACS::ECS::Disk",
                provider: "alicloud",
                native_type: "ACS::ECS::Disk",
                capabilities: ["indexed"],
                bundle_revision: "runtime-a",
                field_display_names: {
                  accountId: { "zh-CN": "账号 ID" },
                },
              },
            ],
            specs: [
              {
                resource_kind: compiledKind,
                revision: "compiled-a",
                hash: "spec-a",
                definition: {
                  metadata: {
                    provider: "alicloud",
                    nativeType: "ACS::ECS::Instance",
                  },
                  scope: { kind: "region" },
                  discovery: { source: "resource-center" },
                },
              },
            ],
          },
        ]),
        {
          status: 200,
          headers: { "Content-Type": "application/json" },
        },
      ),
    ),
  );

  const catalog = await listProviderCatalog();
  const dialogKind = catalog
    .flatMap((bundle) => bundle.specs)
    .find(
      (compiled) => compiled.resource_kind.id === "alicloud:ACS::ECS::Instance",
    )?.resource_kind;

  expect(dialogKind?.field_display_names?.accountId?.["zh-CN"]).toBe("账号 ID");
  expect(catalog[0].kinds).toHaveLength(2);
  expect(catalog[0].revision).toBe("compiled-a");
});
