import { FormEvent, useState } from "react";
import { Play } from "lucide-react";
import type { Copy } from "../../lib/i18n";
import { useCreateScan } from "../../lib/queries";

export function NewScanForm({
  copy,
  onCreated,
}: {
  copy: Copy;
  onCreated: (scanId: string) => void;
}) {
  const [accountName, setAccountName] = useState("local-demo");
  const [mode, setMode] = useState<"demo" | "alicloud">("demo");
  const [regions, setRegions] = useState("cn-hangzhou");
  const [accessKeyId, setAccessKeyId] = useState("");
  const [accessKeySecret, setAccessKeySecret] = useState("");
  const createScan = useCreateScan();

  async function submit(event: FormEvent) {
    event.preventDefault();
    const job = await createScan.mutateAsync({
      accountName,
      provider: mode === "demo" ? "demo" : "alicloud",
      mode,
      regions: regions
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean),
      accessKeyId: mode === "alicloud" ? accessKeyId : undefined,
      accessKeySecret: mode === "alicloud" ? accessKeySecret : undefined,
    });
    onCreated(job.id);
  }

  return (
    <section className="panel">
      <h2 className="panel-title">{copy.sections.newScan}</h2>
      <form className="space-y-4" onSubmit={submit}>
        <label className="field">
          <span>{copy.fields.account}</span>
          <input
            value={accountName}
            onChange={(event) => setAccountName(event.target.value)}
          />
        </label>

        <div className="segmented">
          <button
            type="button"
            className={mode === "demo" ? "active" : ""}
            onClick={() => setMode("demo")}
          >
            Demo
          </button>
          <button
            type="button"
            className={mode === "alicloud" ? "active" : ""}
            onClick={() => setMode("alicloud")}
          >
            Alibaba Cloud
          </button>
        </div>

        <label className="field">
          <span>{copy.fields.regions}</span>
          <input
            value={regions}
            onChange={(event) => setRegions(event.target.value)}
          />
        </label>

        {mode === "alicloud" && (
          <div className="space-y-3">
            <label className="field">
              <span>{copy.fields.accessKeyID}</span>
              <input
                value={accessKeyId}
                onChange={(event) => setAccessKeyId(event.target.value)}
              />
            </label>
            <label className="field">
              <span>{copy.fields.accessKeySecret}</span>
              <input
                type="password"
                value={accessKeySecret}
                onChange={(event) => setAccessKeySecret(event.target.value)}
              />
            </label>
          </div>
        )}

        <button
          className="primary-button w-full"
          disabled={createScan.isPending}
        >
          <Play size={17} />
          <span>{copy.actions.startScan}</span>
        </button>
      </form>
    </section>
  );
}
