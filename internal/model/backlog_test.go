package model

import "testing"

func TestRepairBacklogStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   BacklogStatus
		want BacklogStatus
	}{
		{"", BacklogStatusOpen},
		{BacklogStatusOpen, BacklogStatusOpen},
		{BacklogStatusInProgress, BacklogStatusInProgress},
		{BacklogStatusDone, BacklogStatusDone},
		{BacklogStatusWontFix, BacklogStatusWontFix},
		{"unrecognized", BacklogStatusOpen},
		{"OPEN", BacklogStatusOpen}, // case-sensitive: not a match → repair
	}

	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			t.Parallel()
			item := &BacklogItem{Status: tc.in}
			RepairBacklogStatus(item)
			if item.Status != tc.want {
				t.Errorf("RepairBacklogStatus(%q): Status = %q, want %q", tc.in, item.Status, tc.want)
			}
		})
	}
}
