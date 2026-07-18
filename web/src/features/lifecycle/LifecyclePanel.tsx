import type { Asset, LifecycleBinding } from "@/api/types";
import { JsonViewer } from "@/components/domain/JsonViewer";
import { resourceTypeName } from "@/components/domain/resourceKindLabel";
import { Badge } from "@/components/ui/badge";
import { useLocale } from "@/i18n/LocaleProvider";
import { projectLifecycle } from "./projection";

export function LifecyclePanel({
  focus,
  assets,
  bindings,
}: {
  focus: Asset;
  assets: Asset[];
  bindings: LifecycleBinding[];
}) {
  const { t } = useLocale();
  const labels = {
    delegated: t("lifecycle.delegated"),
    retained: t("lifecycle.retained"),
    unknown: t("lifecycle.unknown"),
  };
  const projection = projectLifecycle(focus.id, [focus, ...assets], bindings);
  const parents = bindings.filter(
    (binding) => binding.managed_asset_id === focus.id,
  );
  if (projection.groups.length === 0 && parents.length === 0)
    return (
      <div className="grid min-h-48 place-items-center text-sm text-muted-foreground">
        {t("lifecycle.empty")}
      </div>
    );
  return (
    <section className="space-y-4">
      {parents.length > 0 && (
        <section>
          <h3 className="mb-3 text-sm font-semibold">
            {t("lifecycle.controller")}
          </h3>
          <div className="grid gap-3 lg:grid-cols-2">
            {parents.map((binding) => (
              <BindingCard
                key={
                  binding.id ||
                  `${binding.controller_asset_id}-${binding.managed_asset_id}`
                }
                binding={binding}
                asset={assets.find(
                  (asset) => asset.id === binding.controller_asset_id,
                )}
                expected={t("lifecycle.cleanThroughController")}
              />
            ))}
          </div>
        </section>
      )}
      {projection.groups.map((group) => (
        <section key={group.key}>
          <h3 className="mb-3 text-sm font-semibold">{labels[group.key]}</h3>
          <div className="grid gap-3 lg:grid-cols-2">
            {group.items.map((item) => (
              <BindingCard
                key={item.binding.id || item.asset.id}
                binding={item.binding}
                asset={assets.find((asset) => asset.id === item.asset.id)}
                expected={item.expected}
                billing={item.possibleBillingResidual}
              />
            ))}
          </div>
        </section>
      ))}
    </section>
  );
}

function BindingCard({
  binding,
  asset,
  expected,
  billing,
}: {
  binding: LifecycleBinding;
  asset?: Asset;
  expected: string;
  billing?: boolean;
}) {
  const { label, locale, t } = useLocale();
  return (
    <article className="space-y-4 rounded-xl border px-4 py-3">
      <div className="flex items-start justify-between gap-3 text-sm font-semibold">
        <span className="min-w-0">
          <span className="block truncate">
            {asset?.name ||
              asset?.identity.native_id ||
              binding.managed_asset_id}
          </span>
          <span className="mt-1 block truncate font-mono text-[11px] font-normal text-muted-foreground">
            {asset
              ? resourceTypeName(
                  asset.identity.native_type,
                  asset.identity.native_type,
                  undefined,
                  locale,
                )
              : binding.managed_asset_id}
          </span>
        </span>
        {billing && (
          <Badge variant="destructive">{t("cleanup.possible")}</Badge>
        )}
      </div>
      <dl className="grid grid-cols-2 gap-3 text-xs">
        {[
          [t("lifecycle.ownership"), label(binding.ownership)],
          [t("lifecycle.cleanup"), label(binding.cleanup_policy)],
          [t("lifecycle.authority"), label(binding.authority)],
          [
            t("lifecycle.confidence"),
            `${Math.round(binding.confidence * 100)}%`,
          ],
          [t("cleanup.expected"), label(expected)],
          [
            t("cleanup.billingResidual"),
            billing ? t("cleanup.possible") : t("cleanup.notIndicated"),
          ],
        ].map(([factLabel, value]) => (
          <div key={factLabel}>
            <dt className="text-muted-foreground">{factLabel}</dt>
            <dd className="mt-1">{value}</dd>
          </div>
        ))}
      </dl>
      <JsonViewer
        value={binding.evidence}
        labels={{ show: t("cleanup.evidence"), hide: t("common.hideJson") }}
      />
    </article>
  );
}
