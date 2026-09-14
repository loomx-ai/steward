# Cloud Monitoring Uptime Check evidence

The retained Monitoring v3 Discovery fragment has revision `20260827`, full-source
SHA-256 `e0bbea1b5a2a0b8af6f1a7528cffd239f27138891a1711417800963e4430db5e`.
It already contains UptimeCheckConfig LIST/GET/DELETE and their native schemas.
No additional source, generated catalog or module dependency change is needed.
With the subsequent AlertPolicy fragment the catalog contains 201 kinds and 788
operations, SHA-256
`0d7ba03c095a042eb22b677e7e4cc82cda08bbea73d929732d7c0182fa95c58c`.

## Native contract and implementation

The [resource schema](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.uptimeCheckConfigs)
defines a server-assigned project-scoped name, one monitored resource/group/synthetic
target, HTTP or TCP configuration, schedules, checker regions and user labels.
Headers encrypted with `maskHeaders` are obscured by native reads. Steward hashes
the observable native configuration before redaction, including unknown fields,
while ignoring the synthetic target's output-only Cloud Run revision. Project ID
and number aliases share a review. LIST identities/configuration must match GET;
malformed, missing or changed detail fails the scan rather than proving absence.
Complete empty LIST establishes absence. Request auth, headers and body are
redacted from stored assets and Invoke output; labels remain queryable.

The [DELETE contract](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.uptimeCheckConfigs/delete)
returns an empty object synchronously and rejects referenced checks. It exposes
no etag, request UUID or operation polling. Steward compares the frozen review in
preflight and again immediately before DELETE. It checks the empty response,
binds its local receipt to the selected asset/configuration/request, and confirms
absence through a separate GET after restart. DELETE 404 requires a matching GET
404; a still-present or changed resource is not success. Existing unreviewed
observations must be rescanned. User-label protection remains effective.

The [synthetic monitor guide](https://docs.cloud.google.com/monitoring/synthetic-monitors/manage)
requires associated alert policies to be removed first and keeps the Cloud Run
function after the check is deleted. This implementation deletes only the check;
it does not delete functions, other monitored resources, metrics or alert policies.
Encrypted values hidden by the API and changes after the final read are outside
the observable review; no atomic precondition or hidden-secret comparison is claimed.

## Retained verification

```sh
go test ./providers/gcp -run 'TestUptime|TestServiceResourceWireLifecycles/monitoring' -count=1
go test -race ./providers/gcp -run TestUptime -count=1
go test ./providers/gcp ./internal/...
go vet ./providers/gcp ./internal/...
node docs/check.mjs
```

Protocol tests cover native schemas and project aliases; HTTP/TCP/group/synthetic
configuration; redaction and queries; page continuations, duplicates, empty and
invalid lists; missing/denied detail; changed targets, headers and unknown fields;
last-read drift; native errors and dependency refusal; synchronous response shape;
receipt tampering, JSON restart and separate absence checks. Actual SQLite workers
verify prior observation preservation, authoritative absence, recovery, one-step
planning, persisted deletion state, restart without a second DELETE, tombstoning
and reconciliation. These tests do not claim production IAM or backend semantics.

The milestone passed full GCP (241.531s), all internal packages including
architecture (26.993s) and cleanup (6.068s), focused race tests (12.201s),
vet, and documentation checks (30 chapters, 10 screenshots) in the isolated
checkout.

## Independent backend boundary

Google Config Connector mockgcp is pinned at
`673a61419de1b8e4f7d26070ce20dde2daa61da8`. The complete, non-truncated subtree
`8b6f0584391a159b43ed7e5adf26580557872854` contains
[uptimecheck.go](https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockmonitoring/uptimecheck.go),
which implements CREATE/GET/UPDATE/DELETE and native defaults/redaction. LIST is
inherited as unimplemented. The opt-in test verifies that LIST fails and observes
an individually read native resource. Reverse Metrics Scope lookup is also
unimplemented: the test first proves that this gap blocks every DELETE, then
explicitly models only that reverse response to exercise the remaining integration.
No substitute Uptime LIST or AlertPolicy response is injected. Native AlertPolicy
CREATE/LIST/GET supplies a referencing policy; Steward blocks the check, deletes
the separately selected policy, verifies 404, then deletes the check and resumes
its prerequisite-bound receipt. The unchanged handlers do not enforce real IAM or
native alert-policy locks and redact every header regardless of maskHeaders;
those limits remain explicit. This is a hybrid test, not complete cloud emulation.

Reuse the retained [Monitoring harness](../metrics-scope/testdata/mockgcp/main.go)
inside a temporary sparse checkout containing `mockgcp`, `pkg`, and
`third_party/github.com/hashicorp/terraform-provider-google-beta` at that revision.
Copy it to `mockgcp/steward-uptime-harness/main.go`, then build from `mockgcp`:

```sh
GOWORK=off go build -o /tmp/steward-uptime-mockgcp ./steward-uptime-harness
/tmp/steward-uptime-mockgcp
```

Use its printed loopback origin in another terminal from Steward's root:

```sh
STEWARD_UPTIME_MOCKGCP_URL=http://127.0.0.1:PORT \
  go test ./providers/gcp -run '^TestUptimeIndependentMockGCP$' -count=1 -v
```

The original independent run, before incoming-scope verification, passed with
seven Monitoring calls (0.690s package time). The current hybrid integration passed
with 26 forwarded calls and six explicitly modeled reverse-scope calls (0.907s).
Both runs verify that unimplemented Uptime LIST fails; tracked upstream handlers
remain unchanged. See [the current evidence](../alert-policy/README.md#validation-of-the-incoming-dependency-milestone).

Stop the temporary process and remove the checkout/binary after verification.
All resources are in memory; OAuth and Resource Manager identity use test fixtures.
Remaining target relationships, Monitoring groups, Logging/MQL/PromQL/SQL reference
analysis and independent LIST or real-cloud acceptance remain unfinished.


## Native target and network relationships

The [monitored-resource descriptors](https://docs.cloud.google.com/monitoring/api/resources)
specify numeric `instance_id` for GCE, project/region/service for Cloud Run,
project/location/namespace/service for Service Directory, and cluster identity
for Kubernetes Services. The [Uptime schema](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.uptimeCheckConfigs)
provides a fully qualified synthetic function, a project-local group ID, and
explicit internal-checker network names with their `peerProjectId`.

Steward derives references only from those target fields and uses full identities
across source/target scopes within the same provider, partition and connection.
Own-project number/ID aliases normalize consistently. A GCE alias binds its numeric
ID to its observed project/zone, preventing a recreated instance with the same
name from satisfying the old target. Network closure follows this alias and the
explicit target references, ignoring arbitrary user-label/HTTP/matcher strings.
The synthetic output-only revision never becomes another configured dependency.

The provider contributor emits dependencies, not ownership or deletion impacts.
When both are selected the check precedes the target; check-only cleanup retains
the target. Missing or foreign-connection targets stay unresolved, and duplicate
identities fail graph construction. Group IDs are retained as unresolved native
Group references until group inventory is implemented. These graph references
alone do not authorize cross-connection or cross-provider deletion.

Protocol/unit tests exercise native field mapping, malformed and encoded paths,
project aliases, foreign projects, cross-scope graph resolution, duplicates,
missing/recreated targets, network closure and arbitrary-payload exclusions.
The SQLite worker test scans a GCE-backed check, preserves its history after an
invalid native target read, persists the cross-scope relationship and deletion
DAG, then deletes only the check through process restarts while retaining the VM.
Server tests verify the contributor is installed once without a service cascade.

This does not yet resolve App Engine/AWS targets, individual Kubernetes Services,
group membership or the implicit legacy `isInternal=true`/empty-checker set.
AlertPolicy inventory and native-filter incoming-reference checks are now covered
by the [Monitoring dependency evidence](../alert-policy/README.md#native-uptime-incoming-dependencies).
Logging/MQL/PromQL/SQL analysis, independent Uptime LIST and full application/live acceptance
remain open. Observed references cannot guarantee atomicity against external edits.
