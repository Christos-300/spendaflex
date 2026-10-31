# ERP sync

Spendaflex can push a spend report to NetSuite. The install's sync path and fallback department live on system settings (`syncPath`, `inactiveDeptFallbackId`). `ExportReport` in `api/internal/services/netsuite_export.go` resolves the department with `resolveDepartment`, sends the entry through a `NetSuiteClient`, and uses `isRetryable` when NetSuite rejects it.

## Sync paths

`legacy-with-dept-fallback` substitutes the account's `inactiveDeptFallbackId` when the report's department is inactive and has no remap. That fallback is G&A, department 100.

`v2-strict` does not substitute. The inactive department id is sent as stored.

An active department is sent unchanged on either path. An inactive department that names a remap target is sent as that target, ahead of either path. Hulu department 233 is inactive and was merged into 234, so a report on 233 exports as 234.

## Netflix and NS_INVALID_REFERENCE

Netflix department 412, Content Operations, is inactive and has no remap. On `v2-strict`, `resolveDepartment` leaves 412 in place. NetSuite rejects the reference with `NS_INVALID_REFERENCE`.

Fourteen fictional Netflix reports sit on that department. Their amounts total $11,474.41.

## Retry status

`isRetryable` treats a NetSuite rejection, including `NS_INVALID_REFERENCE`, as retryable. `statusFor` returns `pending_sync` while attempts remain and `sync_failed` once they are used up.

## Reprocess

`ReprocessExports` checks the named reports before it sends anything. If any department is inactive it returns HTTP 409, code `DEPT_INACTIVE`, and does not change the reports. The body includes `blocking_department_ids` and `blocked_report_ids`. For the Netflix batch those are department 412 and the fourteen report ids.
