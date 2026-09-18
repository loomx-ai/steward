---
title: "OIDC cloud connections"
description: "Exchange workload identities for temporary cloud credentials without uploading long-lived cloud keys."
navTitle: "OIDC connections"
---

Steward uses OIDC workload identity federation: it signs a short-lived workload JWT, the cloud verifies an explicitly configured trust relationship, and returns temporary credentials. AWS, Alibaba Cloud, GCP, and Azure are supported. This is workload authentication, not browser sign-in.

## Operator setup

Configure a stable issuer and an independent RSA signing key for each isolated workspace. Only server configuration can select the issuer and signing key; connection input cannot select token sources, executables, or exchange endpoints.

```sh
umask 077
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out oidc-signing.pem
export STEWARD_OIDC_ISSUER_URL=https://steward.example.com
export STEWARD_OIDC_WORKSPACE_ID=production
export STEWARD_OIDC_SIGNING_KEY_FILE=/absolute/path/oidc-signing.pem
```

The signing file must be private (`0600`) and readable by the service process. The HTTPS issuer must not end with `/`. A path prefix is allowed if the reverse proxy preserves it. OIDC is hidden when unconfigured; partial or invalid configuration fails startup. Do not upload the issuer's signing key as a cloud credential.

Cloud identity services must be able to fetch these documents without a Steward session:

```text
<issuer>/.well-known/openid-configuration
<issuer>/.well-known/jwks
```

These endpoints only publish discovery and public keys. There is no public JWT minting API. Keep workspace APIs authenticated.

### Steward Cloud

The Cloud gateway exposes the two metadata documents under:

```text
https://<console-host>/oidc/workspaces/<workspace-id>/.well-known/openid-configuration
https://<console-host>/oidc/workspaces/<workspace-id>/.well-known/jwks
```

Configure the isolated workspace process with:

```text
STEWARD_OIDC_ISSUER_URL=https://<console-host>/oidc/workspaces/<workspace-id>
STEWARD_OIDC_WORKSPACE_ID=<workspace-id>
STEWARD_OIDC_SIGNING_KEY_FILE=<absolute path to this workspace's signing key>
```

The gateway forwards only these public documents without cookies or service tokens. Workspaces must not share private keys. The existing provisioner does not automatically create signing keys or enable OIDC; operators enable it explicitly. For systemd DynamicUser services, a workspace-specific `LoadCredential` drop-in can provide the key; point the signing-file variable at the service's credentials directory. Keep test and production issuers and keys separate.

## Connect and authorize

1. Open Settings → Cloud connections → Add connection and select OIDC workload identity.
2. Enter the intended cloud role or service identity. You may enter the intended role ARN before creating that role.
3. Save, then expand **OIDC trust configuration** on the connection.
4. Configure cloud trust using the displayed issuer, audience and exact read/write subjects, and grant resource permissions.
5. Validate the connection, then scan.

Subjects use the operator-configured workspace ID and the server-generated connection ID. Renaming a connection preserves its subject. Deleting and recreating it requires updating cloud trust. Do not wildcard other workspaces or connections.

```text
workspace:<workspace-id>:connection:<connection-id>:run_phase:read
workspace:<workspace-id>:connection:<connection-id>:run_phase:write
```

Validation, region discovery, inventory, and dependency analysis use `read`. Durable cleanup execution jobs use `write`. If the optional write identity is empty, both phases use the default identity, which must trust both subjects. Otherwise configure the default identity to trust the read subject and the write identity to trust the write subject.

Connection validation checks the read identity. It does not impersonate a cleanup job to test write privileges. Verify write trust and permissions against test resources before the first production cleanup.

## Cloud configuration

| Cloud | Connection fields | Default audience | Cloud-side configuration |
| --- | --- | --- | --- |
| AWS | Default Role ARN; optional Write Role ARN | `sts.amazonaws.com` | IAM OIDC Provider with this audience as its client ID; role trust permits `sts:AssumeRoleWithWebIdentity` and matches exact `aud` and `sub`. |
| Alibaba Cloud | Default Role ARN, OIDC Provider ARN; optional Write Role ARN | `sts.aliyuncs.com` | RAM OIDC provider; role trust permits `sts:AssumeRoleWithOIDC` and constrains issuer, audience and subject. |
| GCP | Resource project ID, Workload Provider resource name, default service account email; optional write service account | `steward.workload.identity` | Workload Identity Pool/Provider mapping `google.subject` to `assertion.sub`, with this allowed audience; grant the exact principal `roles/iam.workloadIdentityUser` on the service account, then grant that account resource permissions. |
| Azure | Subscription ID, Tenant ID, default Client ID; optional Write Client ID | `api://AzureADTokenExchange` | Application/service principal with Federated Identity Credentials matching the issuer, subject and audience; subscription RBAC assignments. No Client Secret. |

The GCP Workload Provider field is a resource name without a URL prefix:

```text
projects/123456789/locations/global/workloadIdentityPools/steward/providers/production
```

GCP first obtains a federated token, then uses IAM Credentials to impersonate the selected service account. Arbitrary credential JSON, executables and external token URLs are not accepted. Enable the required STS, IAM Credentials and Cloud Asset APIs. The identity pool and resource project may be different projects.

AWS read/write roles must belong to the same account. Alibaba Cloud roles and OIDC provider must belong to the same account. Azure authenticates both clients in the configured tenant against the same subscription. The GCP write service account may be from another project, while the target resource project remains fixed by the connection.

This implementation uses AWS commercial-partition STS, Azure public-cloud Entra/ARM and Google's `googleapis.com` universe. It does not add AWS China/GovCloud, Azure sovereign-cloud or other Google universe support.

### AWS trust policy example

Replace the placeholders with the IAM provider ARN, issuer without `https://`, and subject from the connection. This example is for a read role. Use the write subject for the write role, or an array of both exact subjects when sharing one role.

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

This establishes trust only. Attach a separate permission policy for resource reads, deletion and any related actions.

## Lifetime and rotation

- RS256 JWTs carry `kid`, `iss`, `aud`, `sub`, `iat`, `nbf`, `exp`, a random `jti`, and Steward workspace/connection/provider/phase/run claims. JWT lifetime is five minutes.
- AWS/Alibaba STS and GCP impersonation request one-hour credentials. Azure uses the returned expiry. Temporary credentials are cached by configuration version, phase, run and audience, and refreshed before expiry.
- Assertions and exchanged credentials stay in memory. Only encrypted connection configuration is persisted; no customer long-lived cloud key is required.
- Disabling a connection or replacing its configuration stops further use of its old runtime cache. Already-issued cloud credentials may remain valid until cloud-side expiry; urgent revocation also requires changing cloud trust or permissions.
- Changing the issuer or workspace ID changes trust and is not a routine key rotation.
- A PEM bundle may contain **one current RSA private key** plus previous `PUBLIC KEY` PEM blocks. Restart after publishing the new private key with old public keys: new JWTs use the new `kid`, while JWKS retains old verification keys. Cloud services typically refresh keys on a new `kid`; verify the relying cloud's cache behavior with a test connection. Retain old public keys for at least the old JWT lifetime and the public-key cache window before removing them.

## References

- [AWS OIDC identity providers](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_providers_create_oidc.html)
- [Microsoft Entra workload identity federation](https://learn.microsoft.com/en-us/entra/workload-id/workload-identity-federation)
- [Google Cloud workload identity federation](https://docs.cloud.google.com/iam/docs/workload-identity-federation)
- [Alibaba Cloud OIDC-based SSO](https://www.alibabacloud.com/help/en/ram/user-guide/overview-of-oidc-based-sso)
