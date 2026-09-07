import { audits, catalog, cleanupReview, connection, connectionID, graphEdges, initialAssets, kinds, regions, scan, scopes, topology } from './data.mjs';

// This transport never falls through to fetch. No cloud account or API is used.
export function createDemoTransport() {
  const assets = structuredClone(initialAssets);
  const json = (data, status = 200) => Response.json(data, { status });
  const page = items => json({ items, next_cursor: '' });

  return async function demoFetch(input, init = {}) {
    if (init.signal?.aborted) throw new DOMException('Aborted', 'AbortError');
    const url = new URL(typeof input === 'string' ? input : input.url, 'https://demo.invalid');
    const path = url.pathname;
    const params = url.searchParams;
    const method = init.method || 'GET';
    const zh = typeof document !== 'undefined' && document.documentElement.lang.startsWith('zh');
    if (!path.startsWith('/api/') || (url.origin !== 'https://demo.invalid')) return json({ error: 'Demo request unavailable' }, 404);
    if (method === 'PATCH' && /^\/api\/assets\/[^/]+$/.test(path)) {
      const value = assets.find(value => value.id === decodeURIComponent(path.split('/').at(-1)));
      if (!value) return json({ error: 'Resource not found' }, 404);
      value.dirty = JSON.parse(init.body || '{}').dirty === true;
      return json(value);
    }
    if (method !== 'GET') {
      return json({ error: zh ? '公开演示仅使用示例数据，不支持接入真实云账号。请在自行部署的 Steward 或登录后的 Steward Cloud 中执行此操作。' : 'This public demo uses sample data only and cannot connect real cloud accounts. Use self-hosted Steward or sign in to Steward Cloud to run this action.' }, 403);
    }
    if (params.get('resource_query')) return json({ error: zh ? '公开演示支持按名称或 ID 搜索，不支持高级查询。请在自行部署的 Steward 或登录后的 Steward Cloud 中使用高级查询。' : 'Search this public demo by name or ID. Advanced queries are available in self-hosted Steward or after signing in to Steward Cloud.' }, 400);
    if (path === '/api/session') return json({ mode: 'local', authenticated: true, principal: { subject: 'Demo workspace', roles: ['admin'] } });
    if (path === '/api/connections') return page([connection]);
    if (path === `/api/connections/${connectionID}/regions`) return page(regions);
    if (path === '/api/providers/catalog') return json(catalog);
    if (path === '/api/providers') return json([{ provider: 'alicloud', sites: [{ value: 'intl', label_key: 'provider.site.intl' }], inventory_sources: [{ name: 'product-api' }], credential_schemas: [] }]);
    if (path === '/api/topology') return json(topology(assets, params));
    if (path === '/api/scopes') return page(scopes);
    if (path === '/api/assets') {
      const ids = params.getAll('asset_id');
      const nativeIDs = params.getAll('native_id');
      const kindIDs = params.getAll('resource_kind_id');
      const region = params.get('region_id');
      const vpc = params.get('vpc_id');
      const query = (params.get('q') || params.get('resource_query') || '').toLowerCase();
      return page(assets.filter(value => (!ids.length || ids.includes(value.id))
        && (!nativeIDs.length || nativeIDs.includes(value.identity.native_id))
        && (!kindIDs.length || kindIDs.includes(value.resource_kind_id))
        && (!region || value.location === region)
        && (!vpc || value.normalized.vpc_id === vpc)
        && (!query || `${value.name} ${value.identity.native_id} ${value.identity.native_type}`.toLowerCase().includes(query))));
    }
    const assetMatch = path.match(/^\/api\/assets\/([^/]+)(?:\/(graph|lifecycle))?$/);
    if (assetMatch) {
      const value = assets.find(value => value.id === decodeURIComponent(assetMatch[1]));
      if (!value) return json({ error: 'Resource not found' }, 404);
      if (assetMatch[2] === 'graph') return json({ relationships: graphEdges
        .filter(edge => edge.source_key === value.id || edge.target_key === value.id)
        .map(edge => ({ id: edge.key, source_asset_id: edge.source_key, target_asset_id: edge.target_key,
          type: edge.relation, source: 'demo', confidence: 1, graph_revision: 'demo-1', observed_at: value.last_seen_at })) });
      if (assetMatch[2] === 'lifecycle') return json({ bindings: [] });
      return json(value);
    }
    if (path === '/api/scans') return page([scan]);
    if (path === '/api/scans/demo-scan') return json(scan);
    if (path.endsWith('/logs')) return json({ items: [], next_cursor: '' });
    if (path.endsWith('/events')) return new Response('', { headers: { 'Content-Type': 'text/event-stream' } });
    if (path === '/api/audit-events') return page(audits);
    if (path === '/api/cleanup') return page([cleanupReview.task]);
    if (path === '/api/cleanup/demo-cleanup') return json(cleanupReview);
    if (['/api/findings', '/api/execution-attempts'].includes(path)) return page([]);
    if (path.startsWith('/api/scan-targets/')) return json({ items: [], resource_kinds: kinds, next_cursor: '' });
    return json({ error: 'This view is not available in the demo workspace.' }, 404);
  };
}

export const demoFetch = createDemoTransport();
