import type { CreateConnectionInput, CredentialSchema } from "@/api/types";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { useLocale } from "@/i18n/LocaleProvider";
import type { MessageKey } from "@/i18n/messages";

export function CredentialFields({
  schema,
  values,
  onChange,
}: {
  schema?: CredentialSchema;
  values: Record<string, string>;
  onChange: (values: Record<string, string>) => void;
}) {
  const { t } = useLocale();
  if (!schema) return null;
  return (
    <div className="space-y-4">
      {schema.fields.map((field) => (
        <div className="space-y-2" key={field.key}>
          <Label htmlFor={`credential-${field.key}`}>
            {t(field.label_key as MessageKey)}
          </Label>
          {field.input_type === "textarea" ? (
            <Textarea
              id={`credential-${field.key}`}
              rows={10}
              spellCheck={false}
              autoCorrect="off"
              className="font-mono text-xs"
              required={field.required}
              value={values[field.key] ?? ""}
              onChange={(event) =>
                onChange({ ...values, [field.key]: event.target.value })
              }
              autoComplete="off"
            />
          ) : (
            <Input
              id={`credential-${field.key}`}
              type={field.input_type}
              required={field.required}
              value={values[field.key] ?? ""}
              onChange={(event) =>
                onChange({ ...values, [field.key]: event.target.value })
              }
              autoComplete="off"
            />
          )}
        </div>
      ))}
    </div>
  );
}

export function credentialInputFromValues(
  schema: CredentialSchema,
  values: Record<string, string>,
): CreateConnectionInput["credential"] {
  const expiresAt = values.expires_at
    ? new Date(values.expires_at).toISOString()
    : undefined;
  const credentialValues = Object.fromEntries(
    schema.fields
      .filter((field) => field.key !== "expires_at")
      .map((field) => [field.key, values[field.key] ?? ""]),
  );
  return {
    type: schema.type,
    values: credentialValues,
    expires_at: expiresAt,
  };
}

export function connectionDeletionConfirmed(name: string, value: string) {
  return value === name;
}
