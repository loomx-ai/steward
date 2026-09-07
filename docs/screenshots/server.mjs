// Independent QA: run the unmodified Steward frontend, including its original
// BrowserRouter and HTTP client, against the same synthetic inventory over HTTP.
import { createRequire } from 'node:module';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createDemoTransport } from './transport.mjs';
import { connectionID, initialPath, kinds, scan } from './data.mjs';

const source = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const web = path.resolve(process.env.STEWARD_SOURCE || source, 'web');
const requireSteward = createRequire(path.join(web, 'package.json'));
const { createServer } = await import(pathToFileURL(requireSteward.resolve('vite')));
const { default: react } = await import(pathToFileURL(requireSteward.resolve('@vitejs/plugin-react')));
const { default: tailwindcss } = await import(pathToFileURL(requireSteward.resolve('@tailwindcss/vite')));
const transport = createDemoTransport();
// Completed sample scan for documentation captures, using the real log protocol.
const scanLogs = [
  ['2026-09-06T08:30:00Z', 'Scan started: ap-southeast-1, 6 resource types'],
  ['2026-09-06T08:30:05Z', 'Discovered 9 resources in ap-southeast-1'],
  ['2026-09-06T08:30:12Z', 'Scan completed: 6/6 targets succeeded'],
].map(([created_at, message], index) => ({ id: `demo-log-${index}`, level: 'info', target_key: 'ap-southeast-1', created_at, message }));
const initializer = `const params = new URLSearchParams(location.search);
localStorage.setItem('steward.locale', params.get('locale') === 'en' ? 'en-US' : 'zh-CN');
localStorage.setItem('steward.theme', params.get('theme') === 'dark' ? 'dark' : 'light');
localStorage.setItem('steward.active-connection', ${JSON.stringify(connectionID)});
localStorage.setItem('steward.sidebar-expanded', 'true');
localStorage.removeItem('steward:panorama-cleanup:v1:${connectionID}');`;
process.chdir(web);
const server = await createServer({
  configFile: false, root: web,
  resolve: { alias: { '@': path.join(web, 'src') } },
  server: { host: '127.0.0.1', port: 5859, strictPort: true, fs: { allow: [web] } },
  plugins: [{
    name: 'reference-fixture-server',
    transformIndexHtml: html => html.replace('<head>', `<head><script>${initializer}</script>`),
    configureServer(server) {
      server.middlewares.use(async (req, res, next) => {
        if (req.url.startsWith('/api/')) {
          const pathname = new URL(req.url, 'http://localhost').pathname;
          if (req.method === 'GET' && pathname === '/api/scans/demo-scan/logs') {
            res.setHeader('Content-Type', 'application/json');
            res.end(JSON.stringify({ items: scanLogs, next_cursor: '', live_cursor: 'demo-log-2' }));
            return;
          }
          if (req.method === 'GET' && pathname === '/api/scans/demo-scan/events') {
            res.setHeader('Content-Type', 'text/event-stream');
            res.end(`event: end\ndata: ${JSON.stringify(scan)}\n\n`);
            return;
          }
          const chunks = [];
          for await (const chunk of req) chunks.push(chunk);
          const response = await transport(req.url, { method: req.method, body: Buffer.concat(chunks).toString() });
          res.writeHead(response.status, Object.fromEntries(response.headers));
          res.end(await response.text());
          return;
        }
        const icon = kinds.find(kind => kind.icon === req.url);
        if (icon) {
          res.setHeader('Content-Type', icon.icon.endsWith('.png') ? 'image/png' : 'image/svg+xml');
          res.end(await readFile(path.join(web, 'public', icon.icon.replace('/demo/', ''))));
          return;
        }
        next();
      });
    },
  }, react(), tailwindcss()],
});
await server.listen();
console.log(`Unmodified Steward reference: http://localhost:5859${initialPath}?locale=zh&theme=light`);
