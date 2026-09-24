---
title: "OIDC cloud connections"
description: "Connect AWS, Alibaba Cloud, Google Cloud, or Azure with short-lived credentials from workload identity federation, so no long-lived cloud key is stored in Steward."
navTitle: "OIDC connections"
---

An OIDC connection lets Steward reach your cloud without storing a long-lived access key. Your cloud trusts Steward's server as an identity provider, and every scan or cleanup exchanges a short-lived token for temporary credentials. It works with AWS, Alibaba Cloud, Google Cloud, and Azure.

How it works:

1. The Steward server signs a JWT that identifies the connection and whether it is reading or deleting. The token is valid for five minutes.
2. Your cloud checks the token against a trust relationship you configure: the issuer, audience, and exact subject.
3. The cloud returns temporary credentials for the role or service identity you chose.

This is workload authentication between servers. It is not the same as [browser sign-in](./connections.md#browser).

The **OIDC workload identity** option appears in the connection form only after the server operator has configured it. In Steward Cloud, it appears only if LoomX has enabled it for your workspace.

## Enable OIDC on the server

This step is for whoever runs the Steward server. Give each isolated deployment a stable issuer URL and its own RSA signing key. Only server configuration controls the issuer and key; connection settings cannot choose token sources, executables, or exchange endpoints.

```sh
umask 077
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out oidc-signing.pem
export STEWARD_OIDC_ISSUER_URL=https://steward.example.com
export STEWARD_OIDC_WORKSPACE_ID=production
export STEWARD_OIDC_SIGNING_KEY_FILE=/absolute/path/oidc-signing.pem
```

- The signing key file must be private (`0600`) and readable by the service process. Never upload it as a cloud credential.
- The issuer must use HTTPS and must not end with `/`. It may include a path prefix if your reverse proxy preserves it.
- Without these variables, OIDC stays hidden. Incomplete or invalid configuration stops the server from starting.
- Keep test and production issuers and keys separate.

Your cloud's identity service must be able to fetch these two documents without signing in to Steward:

```text
<issuer>/.well-known/openid-configuration
<issuer>/.well-known/jwks
```

They contain only discovery metadata and public keys; there is no public endpoint that issues tokens. Keep the rest of Steward's API behind authentication.

## Create the connection

1. Open the user menu → **Settings** → **Cloud connections** → **Add connection**, choose your cloud, and select **OIDC workload identity**.
2. Enter the role or service identity Steward should use (see the table below). You can enter a role ARN before the role exists.
3. Save the connection, then expand **OIDC trust configuration** on it. It shows the issuer, audience, and the exact read and write subjects to trust.
4. In your cloud, configure the trust relationship using those values, and grant the identity its resource permissions.
5. Back in Steward, validate the connection, then start a scan.

Subjects combine the server's workspace ID with a connection ID that Steward generates:

```text
workspace:<workspace-id>:connection:<connection-id>:run_phase:read
workspace:<workspace-id>:connection:<connection-id>:run_phase:write
```

- **`read`** is used for validation, region discovery, scans, and dependency analysis. **`write`** is used only by cleanup execution.
- **One identity or two.** If you leave the optional write identity empty, the default identity handles both phases and must trust both subjects. If you set a write identity, the default identity trusts the read subject and the write identity trusts the write subject.
- **Renaming** a connection keeps its subjects. **Deleting and recreating** it creates new ones, so update the cloud trust.
- Match subjects exactly. Do not use wildcards that would also match other workspaces or connections.

Validation checks only the read identity; it does not test cleanup permissions. Before your first real cleanup, run one against test resources to confirm the write trust and permissions.

## Configure trust in your cloud

| Cloud | Connection fields | Default audience | What to configure in the cloud |
| --- | --- | --- | --- |
| AWS | Default Role ARN; optional Write Role ARN | `sts.amazonaws.com` | An IAM OIDC provider with this audience as its client ID. The role's trust policy allows `sts:AssumeRoleWithWebIdentity` and matches `aud` and `sub` exactly. |
| Alibaba Cloud | Default Role ARN, OIDC Provider ARN; optional Write Role ARN | `sts.aliyuncs.com` | A RAM OIDC provider. The role's trust policy allows `sts:AssumeRoleWithOIDC` and constrains issuer, audience, and subject. |
| Google Cloud | Resource project ID, Workload Provider resource name, default service account email; optional write service account | `steward.workload.identity` | A Workload Identity Pool and Provider that maps `google.subject` to `assertion.sub` and allows this audience. Grant the exact principal `roles/iam.workloadIdentityUser` on the service account, then grant that service account its resource permissions. |
| Azure | Subscription ID, Tenant ID, default Client ID; optional Write Client ID | `api://AzureADTokenExchange` | An application or service principal with a federated identity credential matching the issuer, subject, and audience, plus RBAC role assignments on the subscription. No client secret is needed. |

Account and project rules:

- **AWS:** read and write roles must be in the same account.
- **Alibaba Cloud:** the roles and the OIDC provider must be in the same account.
- **Google Cloud:** the identity pool and the resource project can be different projects, and the write service account can come from another project; the connection's resource project stays fixed. Enter the Workload Provider as a resource name without a URL prefix:

  ```text
  projects/123456789/locations/global/workloadIdentityPools/steward/providers/production
  ```

  Steward first obtains a federated token, then uses IAM Credentials to impersonate the service account. Enable the STS, IAM Credentials, and Cloud Asset APIs. Arbitrary credential JSON, executables, and external token URLs are not accepted.
- **Azure:** both clients authenticate in the configured tenant, against the same subscription.

Supported cloud environments: the AWS commercial partition, Azure public cloud (Entra ID and ARM), and Google's `googleapis.com` universe. AWS China, AWS GovCloud, Azure sovereign clouds, and other Google universes are not supported.

### AWS trust policy example

Replace the placeholders with your IAM OIDC provider ARN, the issuer without `https://`, and the subject shown on the connection. This example is for the read role; use the write subject for a write role, or an array of both exact subjects when one role serves both.

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "<IAM_OIDC_PROVIDER_ARN>"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": {
        "<ISSUER_WITHOUT_HTTPS>:aud": "sts.amazonaws.com",
        "<ISSUER_WITHOUT_HTTPS>:sub": "<READ_SUBJECT>"
      }
    }
  }]
}
```

The trust policy only lets Steward assume the role. Attach a separate permission policy for resource reads, deletion, and related actions.

## Credential lifetime and key rotation

- **Tokens.** JWTs are signed with RS256 and carry `kid`, `iss`, `aud`, `sub`, `iat`, `nbf`, `exp`, a random `jti`, and Steward workspace, connection, provider, phase, and run claims. Each is valid for five minutes.
- **Temporary credentials.** AWS and Alibaba Cloud STS and Google Cloud impersonation request one-hour credentials; Azure uses the expiry it returns. Steward caches them per configuration version, phase, run, and audience, and refreshes them before they expire.
- **Storage.** Tokens and temporary credentials stay in memory. Only the encrypted connection configuration is stored; no long-lived cloud key is needed.
- **Revocation.** Disabling a connection or replacing its configuration stops Steward from using the cached credentials. Credentials the cloud already issued can stay valid until they expire, so for urgent revocation also change the cloud trust or permissions.
- **Changing the issuer or workspace ID** changes the trust relationship. It is not a routine key rotation.
- **Rotating the signing key.** The PEM file may contain **one current RSA private key** plus earlier `PUBLIC KEY` blocks. Publish the new private key together with the old public keys and restart: new tokens use the new `kid`, while the JWKS keeps the old keys for verification. Clouds usually refresh keys when they see a new `kid`; confirm this with a test connection first. Remove old public keys only after both the old token lifetime and the cloud's key cache window have passed.

## References

- [AWS OIDC identity providers](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_providers_create_oidc.html)
- [Microsoft Entra workload identity federation](https://learn.microsoft.com/en-us/entra/workload-id/workload-identity-federation)
- [Google Cloud workload identity federation](https://docs.cloud.google.com/iam/docs/workload-identity-federation)
- [Alibaba Cloud OIDC-based SSO](https://www.alibabacloud.com/help/en/ram/user-guide/overview-of-oidc-based-sso)
