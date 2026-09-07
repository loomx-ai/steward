# Steward documentation

The public guide is at [loomx.ai/steward/docs](https://loomx.ai/steward/docs). This repository owns its content and screenshots. The shared `loomx-ai/docs` repository builds and publishes the documentation site independently of the company website.

## Edit the guide

- Edit one Markdown file per chapter and language in `content/en` and `content/zh`.
- `site.json` defines the product name, supported languages, and ordered chapter paths. Add each chapter in every supported language.
- Use relative Markdown links, such as `./connections.md` or `../index.md#start`. The site resolves them within the selected product, version, and language.
- Store images in `assets/` and link to them relative to the Markdown file. Keep screenshots at their original dimensions and include descriptive alt text and a sample-data caption.
- A standalone `<span id="stable-id"></span>` before a heading preserves an existing anchor. The renderer applies that ID to the heading and the page's table of contents.
- Keep implementation notes such as `localstack.md` outside `content/`; they are not published as user documentation.

Run `node docs/check.mjs` before submitting a change. CI checks navigation, translation completeness, relative links, and screenshot integrity.

To preview the full site from a sibling checkout of `loomx-ai/docs`:

```sh
cd ../docs
npm ci
DOCS_PREVIEW=1 DOCS_SOURCES='{"steward":"../steward"}' npm run dev
```

Open `http://127.0.0.1:4321/steward/docs/latest/en/` or replace `en` with `zh`. Local overrides are preview-only and cannot be published through the production deploy command. See the shared site's README for release and rollback procedures.

## Screenshots

The existing images were captured from the unmodified Steward frontend at commit `4a5e9181d8b418dc02049260208d0230de76e871`, at 1440 × 960, in English and Simplified Chinese. `screenshots/source.json` records source provenance and hashes. The HTTP fixtures contain nine synthetic Alibaba Cloud resources and require no cloud credentials.

After installing `web/` dependencies, run `node docs/screenshots/server.mjs`. To reproduce the original UI revision, set `STEWARD_SOURCE` to a checkout of that commit. Open the relevant path at `http://127.0.0.1:5859` with `?locale=en&theme=light` or `?locale=zh&theme=light`:

| Image | Path and state |
| --- | --- |
| `inventory-{locale}.png` | `/assets` |
| `topology-{locale}.png` | `/panorama/regions/ap-southeast-1/vpcs/vpc-demo-production` |
| `relationships-{locale}.png` | Same VPC path; enable **Show relationship lines**. |
| `scan-{locale}.png` | `/scans/demo-scan` |
| `cleanup-{locale}.png` | `/cleanup/demo-cleanup`; open **View blocked resources**, then **Add dependent resources**. Capture the dialog listing `api-02`, without submitting. |

Wait for the data to render. Keep the actual UI, review each language separately, and update `screenshots/source.json` when replacing an image. Never include real credentials, account identifiers, or customer data.
