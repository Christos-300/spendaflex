package services

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
	"receipt-wrangler/api/internal/utils"
)

type recordingNetSuite struct {
	entries []JournalEntry
	reject  map[uint]string
}

func (c *recordingNetSuite) Send(entry JournalEntry) error {
	c.entries = append(c.entries, entry)
	if code, ok := c.reject[entry.DepartmentID]; ok {
		return &netSuiteRejection{Code: code}
	}
	return nil
}

type memoryReports struct {
	reports []Report
}

func (m *memoryReports) FindReports(ids []uint) ([]Report, error) {
	byID := make(map[uint]Report, len(m.reports))
	for _, report := range m.reports {
		byID[report.ID] = report
	}

	found := make([]Report, 0, len(ids))
	for _, id := range ids {
		report, ok := byID[id]
		if !ok {
			continue
		}
		found = append(found, report)
	}
	return found, nil
}

func uintPtr(value uint) *uint {
	return &value
}

func TestExportReport_ActiveDepartmentPassesThrough(t *testing.T) {
	client := &recordingNetSuite{}
	service := NewNetSuiteExportService(client, nil)
	report := Report{
		ID:     31001,
		Name:   "Animation overtime",
		Amount: decimal.RequireFromString("240.00"),
		Department: Department{
			ID:     310,
			Name:   "Animation",
			Active: true,
		},
		Status:      "ready",
		MaxAttempts: 3,
	}

	got, err := service.ExportReport(report, Account{
		Name:     "Northwind Pictures",
		SyncPath: "v2-strict",
	})
	if err != nil {
		utils.PrintTestError(t, err, "no error")
	}
	if len(client.entries) != 1 {
		t.Fatalf("expected 1 send, got %d", len(client.entries))
	}
	if client.entries[0].DepartmentID != 310 {
		utils.PrintTestError(t, client.entries[0].DepartmentID, uint(310))
	}
	if got.Status != "ready" {
		utils.PrintTestError(t, got.Status, "ready")
	}
}

func TestExportReport_InactiveDepartmentRemaps(t *testing.T) {
	client := &recordingNetSuite{}
	service := NewNetSuiteExportService(client, nil)
	fallback := uint(100)
	report := Report{
		ID:     23301,
		Name:   "Hulu pilot pickup",
		Amount: decimal.RequireFromString("1500.00"),
		Department: Department{
			ID:      233,
			Name:    "Originals",
			Active:  false,
			RemapTo: uintPtr(234),
		},
		MaxAttempts: 3,
	}

	_, err := service.ExportReport(report, Account{
		Name:                   "Hulu",
		SyncPath:               "legacy-with-dept-fallback",
		InactiveDeptFallbackId: &fallback,
	})
	if err != nil {
		utils.PrintTestError(t, err, "no error")
	}
	if len(client.entries) != 1 {
		t.Fatalf("expected 1 send, got %d", len(client.entries))
	}
	if client.entries[0].DepartmentID != 234 {
		utils.PrintTestError(t, client.entries[0].DepartmentID, uint(234))
	}
}

func TestExportReport_LegacyFallbackToGeneralAndAdmin(t *testing.T) {
	client := &recordingNetSuite{}
	service := NewNetSuiteExportService(client, nil)
	fallback := uint(100)
	report := Report{
		ID:     8801,
		Name:   "Closed set strike",
		Amount: decimal.RequireFromString("640.00"),
		Department: Department{
			ID:     88,
			Name:   "Closed Productions",
			Active: false,
		},
		MaxAttempts: 3,
	}

	_, err := service.ExportReport(report, Account{
		Name:                   "Northwind Pictures",
		SyncPath:               "legacy-with-dept-fallback",
		InactiveDeptFallbackId: &fallback,
	})
	if err != nil {
		utils.PrintTestError(t, err, "no error")
	}
	if len(client.entries) != 1 {
		t.Fatalf("expected 1 send, got %d", len(client.entries))
	}
	if client.entries[0].DepartmentID != 100 {
		utils.PrintTestError(t, client.entries[0].DepartmentID, uint(100))
	}
}

func TestExportReport_V2StrictSendsInactiveDepartment(t *testing.T) {
	client := &recordingNetSuite{reject: map[uint]string{412: netSuiteInvalidReference}}
	service := NewNetSuiteExportService(client, nil)
	report := Report{
		ID:     41201,
		Name:   "Netflix stage rental",
		Amount: decimal.RequireFromString("820.15"),
		Department: Department{
			ID:     412,
			Name:   "Content Operations",
			Active: false,
		},
		MaxAttempts: 3,
	}

	got, err := service.ExportReport(report, Account{
		Name:     "Netflix",
		SyncPath: "v2-strict",
	})
	if err == nil {
		t.Fatal("expected NS_INVALID_REFERENCE")
	}
	var rejection *netSuiteRejection
	if !errors.As(err, &rejection) || rejection.Code != "NS_INVALID_REFERENCE" {
		utils.PrintTestError(t, err, "NS_INVALID_REFERENCE")
	}
	if len(client.entries) != 1 {
		t.Fatalf("expected 1 send, got %d", len(client.entries))
	}
	if client.entries[0].DepartmentID != 412 {
		utils.PrintTestError(t, client.entries[0].DepartmentID, uint(412))
	}
	if got.Status != "pending_sync" {
		utils.PrintTestError(t, got.Status, "pending_sync")
	}
}

func TestIsRetryable_NetSuiteInvalidReference(t *testing.T) {
	err := &netSuiteRejection{Code: "NS_INVALID_REFERENCE"}
	if !isRetryable(err) {
		utils.PrintTestError(t, isRetryable(err), true)
	}
}

func TestStatusFor_PendingWhileRetryingAndFailedWhenExhausted(t *testing.T) {
	tests := map[string]struct {
		report Report
		want   string
	}{
		"retrying": {
			report: Report{Attempts: 1, MaxAttempts: 3},
			want:   "pending_sync",
		},
		"exhausted": {
			report: Report{Attempts: 3, MaxAttempts: 3},
			want:   "sync_failed",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := statusFor(test.report)
			if got != test.want {
				utils.PrintTestError(t, got, test.want)
			}
		})
	}
}

func TestReprocessExports_InactiveDepartmentConflicts(t *testing.T) {
	reports := netflixContentOperationsReports()
	before := append([]Report(nil), reports...)
	lookup := &memoryReports{reports: reports}
	client := &recordingNetSuite{}
	service := NewNetSuiteExportService(client, lookup)

	ids := make([]uint, len(reports))
	total := decimal.Zero
	for i, report := range reports {
		ids[i] = report.ID
		total = total.Add(report.Amount)
	}
	if !total.Equal(decimal.RequireFromString("11474.41")) {
		utils.PrintTestError(t, total.String(), "11474.41")
	}

	err := service.ReprocessExports(ids)
	var conflict *DeptInactiveError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected DEPT_INACTIVE, got %v", err)
	}
	if conflict.StatusCode != 409 {
		utils.PrintTestError(t, conflict.StatusCode, 409)
	}
	if conflict.Code != "DEPT_INACTIVE" {
		utils.PrintTestError(t, conflict.Code, "DEPT_INACTIVE")
	}
	if len(conflict.BlockingDepartmentIds) != 1 || conflict.BlockingDepartmentIds[0] != 412 {
		t.Fatalf("blocking_department_ids = %v, want [412]", conflict.BlockingDepartmentIds)
	}
	if len(conflict.BlockedReportIds) != 14 {
		t.Fatalf("blocked_report_ids count = %d, want 14", len(conflict.BlockedReportIds))
	}
	for i, id := range ids {
		if conflict.BlockedReportIds[i] != id {
			t.Fatalf("blocked_report_ids[%d] = %d, want %d", i, conflict.BlockedReportIds[i], id)
		}
	}

	body, marshalErr := json.Marshal(conflict)
	if marshalErr != nil {
		t.Fatalf("marshal conflict: %v", marshalErr)
	}
	var decoded map[string]any
	if unmarshalErr := json.Unmarshal(body, &decoded); unmarshalErr != nil {
		t.Fatalf("unmarshal conflict: %v", unmarshalErr)
	}
	if _, ok := decoded["blocking_department_ids"]; !ok {
		t.Fatal("response body missing blocking_department_ids")
	}
	if _, ok := decoded["blocked_report_ids"]; !ok {
		t.Fatal("response body missing blocked_report_ids")
	}

	if len(client.entries) != 0 {
		t.Fatalf("expected no sends, got %d", len(client.entries))
	}
	if !reflect.DeepEqual(lookup.reports, before) {
		t.Fatal("reprocess changed report state")
	}
}

func netflixContentOperationsReports() []Report {
	amounts := []string{
		"820.15",
		"815.40",
		"802.22",
		"799.05",
		"910.33",
		"640.18",
		"875.60",
		"733.27",
		"688.91",
		"954.44",
		"701.08",
		"866.52",
		"792.17",
		"1075.09",
	}
	names := []string{
		"Stage craft rental",
		"Wardrobe overtime",
		"Location permit",
		"Catering day 3",
		"Camera package",
		"Grip truck",
		"Studio hold",
		"Extras payroll",
		"Set dressing",
		"Lighting package",
		"Prop house",
		"Vanities",
		"Playback operator",
		"Picture-car rental",
	}

	department := Department{ID: 412, Name: "Content Operations", Active: false}
	reports := make([]Report, len(amounts))
	for i, amount := range amounts {
		reports[i] = Report{
			ID:          uint(14001 + i),
			Name:        names[i],
			Amount:      decimal.RequireFromString(amount),
			Department:  department,
			Status:      "sync_failed",
			Attempts:    3,
			MaxAttempts: 3,
		}
	}
	return reports
}
