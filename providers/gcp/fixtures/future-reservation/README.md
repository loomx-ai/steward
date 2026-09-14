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

These are scripted native protocol tests, not live-cloud recordings. Google's
pinned [mockgcp implementation](https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockcompute/futurereservationsv1.go)
provides FutureReservations GET, INSERT, UPDATE, CANCEL, DELETE and aggregated
listing with native zonal operations. Inspection establishes a usable candidate
for the next independent-server check; this milestone does not claim an executed
FutureReservation emulator test. That implementation does not paginate aggregated
results or reproduce real procurement/fulfillment, IAM and scheduling behavior.
Broader lifecycle and cloud-equivalence acceptance remain open.
