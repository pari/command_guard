package main

import (
	"reflect"
	"testing"
)

func TestSortScheduledJobStatuses(t *testing.T) {
	jobs := []ScheduledJobStatus{
		{CmdID: "monday-job", NextRun: "2026-10-19 09:00:00"},
		{CmdID: "never", NextRun: "0001-01-01 00:00:00"},
		{CmdID: "zipfile-every-minute", NextRun: "2026-10-16 16:03:00"},
		{CmdID: "afternoon", NextRun: "2026-10-16 17:00:00"},
		{CmdID: "empty", NextRun: ""},
		{CmdID: "backup-every-minute", NextRun: "2026-10-16 16:03:00"},
		{CmdID: "tomorrow", NextRun: "2026-10-17 01:05:00"},
	}
	sortScheduledJobStatuses(jobs)

	var got []string
	for _, job := range jobs {
		got = append(got, job.CmdID)
	}
	want := []string{"backup-every-minute", "zipfile-every-minute", "afternoon", "tomorrow", "monday-job", "empty", "never"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected order\n got: %v\nwant: %v", got, want)
	}
}
