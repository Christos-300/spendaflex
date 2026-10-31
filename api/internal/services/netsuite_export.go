package services

import (
	"errors"
	"net/http"

	"github.com/shopspring/decimal"
	"receipt-wrangler/api/internal/logging"
	"receipt-wrangler/api/internal/models"
)

const (
	netSuiteInvalidReference = "NS_INVALID_REFERENCE"
	deptInactiveCode         = "DEPT_INACTIVE"
	statusPendingSync        = "pending_sync"
	statusSyncFailed         = "sync_failed"
)

// NetSuiteClient is the export destination. Implementations used in production
// talk to NetSuite; tests pass a fake. This package does not open a connection
// or read credentials.
type NetSuiteClient interface {
	Send(entry JournalEntry) error
}

// ReportLookup loads the reports a reprocess request names.
type ReportLookup interface {
	FindReports(ids []uint) ([]Report, error)
}

// JournalEntry is one report as NetSuite receives it, after department resolution.
type JournalEntry struct {
	ReportID     uint
	DepartmentID uint
	Amount       decimal.Decimal
}

// Account is the install's export settings: the sync path and, for the legacy
// path, the department that stands in for an inactive one.
type Account struct {
	Name                   string
	SyncPath               string
	InactiveDeptFallbackId *uint
}

// Department is the report's department at export time. RemapTo is set when an
// inactive department was merged into another.
type Department struct {
	ID      uint
	Name    string
	Active  bool
	RemapTo *uint
}

// Report is one exportable spend report.
type Report struct {
	ID          uint
	Name        string
	Amount      decimal.Decimal
	Department  Department
	Status      string
	Attempts    int
	MaxAttempts int
}

// DeptInactiveError is the 409 returned when a reprocess batch still names an
// inactive department. Nothing in the batch is changed.
type DeptInactiveError struct {
	StatusCode            int    `json:"-"`
	Code                  string `json:"code"`
	BlockingDepartmentIds []uint `json:"blocking_department_ids"`
	BlockedReportIds      []uint `json:"blocked_report_ids"`
}

func (e *DeptInactiveError) Error() string {
	return e.Code
}

type netSuiteRejection struct {
	Code string
}

func (e *netSuiteRejection) Error() string {
	return e.Code
}

// NetSuiteExportService exports reports and reprocesses a batch. An active
// department is sent unchanged. An inactive department with a remap is sent as
// the remap target. Otherwise legacy-with-dept-fallback substitutes the
// account's inactive department fallback, and v2-strict sends the inactive id.
// ReprocessExports refuses the whole batch when any department is inactive.
type NetSuiteExportService struct {
	client  NetSuiteClient
	reports ReportLookup
}

func NewNetSuiteExportService(client NetSuiteClient, reports ReportLookup) NetSuiteExportService {
	return NetSuiteExportService{client: client, reports: reports}
}

// ExportReport resolves the department, sends the entry, and on a NetSuite
// rejection either leaves the report pending another attempt or marks it failed.
func (service NetSuiteExportService) ExportReport(report Report, account Account) (Report, error) {
	if service.client == nil {
		return report, errors.New("netsuite client is not configured")
	}

	departmentID := resolveDepartment(report.Department, account)
	err := service.client.Send(JournalEntry{
		ReportID:     report.ID,
		DepartmentID: departmentID,
		Amount:       report.Amount,
	})
	if err == nil {
		return report, nil
	}

	logging.LogStd(logging.LOG_LEVEL_ERROR, "NetSuite export rejected: ", err.Error())
	report.Attempts++
	if isRetryable(err) {
		report.Status = statusFor(report)
		return report, err
	}

	report.Status = statusSyncFailed
	return report, err
}

// resolveDepartment picks the department id NetSuite is given.
func resolveDepartment(department Department, account Account) uint {
	if department.Active {
		return department.ID
	}

	if department.RemapTo != nil {
		return *department.RemapTo
	}

	if account.SyncPath == models.SyncPathLegacyWithDeptFallback && account.InactiveDeptFallbackId != nil {
		return *account.InactiveDeptFallbackId
	}

	// v2-strict, and a legacy account with no fallback id, sends the inactive
	// department unchanged. NetSuite rejects that reference.
	return department.ID
}

func isRetryable(err error) bool {
	var rejection *netSuiteRejection
	return errors.As(err, &rejection)
}

func statusFor(report Report) string {
	if report.Attempts >= report.MaxAttempts {
		return statusSyncFailed
	}

	return statusPendingSync
}

// ReprocessExports rejects the batch with 409 DEPT_INACTIVE when any report's
// department is inactive, and does not change those reports.
func (service NetSuiteExportService) ReprocessExports(ids []uint) error {
	if service.reports == nil {
		return errors.New("report lookup is not configured")
	}

	reports, err := service.reports.FindReports(ids)
	if err != nil {
		return err
	}

	blocking := make([]uint, 0)
	blocked := make([]uint, 0)
	seen := make(map[uint]struct{})

	for _, report := range reports {
		if report.Department.Active {
			continue
		}

		blocked = append(blocked, report.ID)
		if _, ok := seen[report.Department.ID]; ok {
			continue
		}
		seen[report.Department.ID] = struct{}{}
		blocking = append(blocking, report.Department.ID)
	}

	if len(blocked) == 0 {
		return nil
	}

	return &DeptInactiveError{
		StatusCode:            http.StatusConflict,
		Code:                  deptInactiveCode,
		BlockingDepartmentIds: blocking,
		BlockedReportIds:      blocked,
	}
}
