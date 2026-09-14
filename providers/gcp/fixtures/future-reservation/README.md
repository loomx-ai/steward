# Future Reservation inventory verification

The native [FutureReservation resource](https://docs.cloud.google.com/compute/docs/reference/rest/v1/futureReservations/get)
uses `status.procurementStatus` for procurement state. `planningStatus` describes
submission, and `status.lastKnownGoodState` describes amendment rollback history.
Neither substitutes for the current procurement state. Inventory and action
readback share the state parser; unknown future string states remain visible.

The resource rule exposes requested instance count, fulfilled count, resolved
instance-template metadata, matching usage, lock/amendment details, time windows,
commitment terms and native storage-pool capacity objects. Declared fields are
materialized in normalized inventory for property queries while the native
payload remains available. Native int64 quantities stay strings, including values
above JavaScript's exact integer range. Storage capacity is not an instance count.

`status.autoCreatedReservations` contains URLs of generated reservations.
These URLs and `sourceInstanceTemplate` are creation metadata, not evidence of
live deletion dependencies. No additional child or foreign-project reads are
introduced. Storage pool capacity/type fields do not identify a concrete pool.
`autoDeleteAutoCreatedReservations` schedules removal at the reservation end or
specified time/duration; it is not a cascade on FutureReservation DELETE.
The existing own-resource DELETE, operation polling and absence readback remain.
No automatic cancellation or generated-reservation deletion is added.

## Evidence and reproduction

Tests consume the existing selected Compute v1 schemas directly from
`catalog/source/discovery.json`, pinned to upstream response SHA-256
`5cee2d2fedf69f756fbc23aaef9153db01c2a2caffdbd6f63681912b65ca8139`
(revision `20260828`, [Discovery source](https://www.googleapis.com/discovery/v1/apis/compute/v1/rest)).
The independent JSON Schema validator checks synthetic response shapes and each
declared native property path/type. No new API document, operation or schema
fixture copy is needed. The 194 resource rules and 773 operations are unchanged.

```sh
go test ./providers/gcp -run 'TestFutureReservation|TestComputeExtendedResourceWireLifecycles' -count=1
go test -race ./providers/gcp ./internal/app/inventory -run 'TestFutureReservation|TestComputeExtended|TestProjectionPreservesNativeFieldsAndDisplayMetadata' -count=1
```

Coverage includes all 13 pinned procurement states, unknown-state compatibility,
planning/amendment separation, empty-page continuation, aggregated regional and
project scans, foreign-project rejection, exact counts, storage capacity and
public property metadata. Native lifecycle tests exercise only the future
reservation's mutation, polling and own readback, despite generated-reservation
URLs in its payload. SQLite runs registered scan workers, reopens the database
and runtime, preserves observations through permission/partial/malformed-list
failures, then updates fulfillment and reconciles complete-list absence.

## Independent Google server

`TestFutureReservationIndependentMockGCP` also passed against the unmodified
[Google mockgcp implementation](https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockcompute/futurereservationsv1.go)
at commit `673a61419de1b8e4f7d26070ce20dde2daa61da8`. Its service source SHA-256 is
`add8883572e0c88d3e446e65f61c3275c34d470a11dc40f3ad63cb1178f30865`.
The existing firewall-policy Compute harness supplies in-memory upstream storage
and two fixture project identities; it does not replace service methods.

The test creates one reservation in each of two zones using the server's native
INSERT and zonal operation API. It checks server-generated DRAFTING/DRAFT states,
resource IDs, matching-usage count and duration-derived end/deletion timestamps.
A native fixture-side CANCEL verifies the refreshed procurement state; Steward
cleanup does not automatically cancel reservations. Native project and regional
aggregated lists prove selection, including preservation of the other reservation
after the first deletion.

Steward forwards 18 Compute calls without modifying their responses: six
aggregated lists, two operation polls, eight own-resource reads and two DELETEs.
Six additional native setup calls create/cancel fixtures and poll their operations.
Each deletion uses its expected UUID request ID; serialized requests and receipts
resume in new runtimes, and both own GETs and the final aggregate list prove
absence. Acknowledged deletes are not replayed. The test cleans only its uniquely
named resources, including on failure. OAuth and CRM project discovery still use
explicit fixtures. No Compute list, detail, mutation, operation or absence shim
is used.

To reproduce from the Steward repository root, use an isolated checkout of the
pinned upstream commit. Its repository-local dependency directories are required;
no upstream service code or module definitions need editing:

```sh
steward_root="$PWD"
mock_root="$(mktemp -d)"
git -C "$mock_root" init -q
git -C "$mock_root" remote add origin https://github.com/GoogleCloudPlatform/k8s-config-connector.git
git -C "$mock_root" sparse-checkout init --cone
git -C "$mock_root" sparse-checkout set mockgcp pkg apis third_party config operator version scripts
git -C "$mock_root" fetch --filter=blob:none --depth=1 origin 673a61419de1b8e4f7d26070ce20dde2daa61da8
git -C "$mock_root" checkout --detach FETCH_HEAD
mkdir -p "$mock_root/mockgcp/cmd/steward-future-reservation"
cp "$steward_root/providers/gcp/fixtures/firewall-policy/testdata/mockgcp/main.go" "$mock_root/mockgcp/cmd/steward-future-reservation/main.go"
GOWORK=off go -C "$mock_root/mockgcp" build -o "$mock_root/server" ./cmd/steward-future-reservation
"$mock_root/server"
```

Use the printed loopback port in another terminal at the Steward repository root:

```sh
STEWARD_FUTURE_RESERVATION_MOCKGCP_URL=http://127.0.0.1:PORT \
  go test ./providers/gcp -run '^TestFutureReservationIndependentMockGCP$' -count=1 -v
STEWARD_FUTURE_RESERVATION_MOCKGCP_URL=http://127.0.0.1:PORT \
  go test -race ./providers/gcp -run 'TestFutureReservation|TestComputeExtendedResourceWireLifecycles' -count=1
```

Stop the server and remove the temporary upstream checkout and binary afterward.
Without the environment variable, the independent test skips explicitly; the
regular protocol suite remains available without an emulator or new dependency.

The independent implementation does not paginate aggregated results or reproduce
real procurement/fulfillment, IAM and scheduling behavior. Those behaviors need
protocol or cloud evidence as appropriate. None of these fixtures is a live-cloud
recording. Broader lifecycle and cloud-equivalence acceptance remain open.
