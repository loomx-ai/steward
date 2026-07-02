import type { Copy } from "../../lib/i18n";
import type { Resource } from "../../lib/api";

export function ResourceDetail({
  resource,
  copy,
}: {
  resource: Resource | null;
  copy: Copy;
}) {
  if (!resource) {
    return (
      <aside className="detail-panel">
        <h3>{copy.sections.resourceDetail}</h3>
        <div className="empty">{copy.empty.noResourceSelected}</div>
      </aside>
    );
  }
  return (
    <aside className="detail-panel">
      <h3>{copy.sections.resourceDetail}</h3>
      <dl>
        <dt>{copy.table.name}</dt>
        <dd>{resource.name}</dd>
        <dt>{copy.fields.nativeID}</dt>
        <dd>{resource.native_id}</dd>
        <dt>{copy.fields.protected}</dt>
        <dd>{resource.protected ? copy.fields.yes : copy.fields.no}</dd>
        <dt>{copy.fields.tags}</dt>
        <dd>
          {Object.keys(resource.tags).length === 0 ? (
            "-"
          ) : (
            <div className="tag-grid">
              {Object.entries(resource.tags).map(([key, value]) => (
                <span key={key}>
                  {key}={value}
                </span>
              ))}
            </div>
          )}
        </dd>
        <dt>{copy.fields.raw}</dt>
        <dd>
          <pre>{JSON.stringify(resource.raw, null, 2)}</pre>
        </dd>
      </dl>
    </aside>
  );
}
