package processlist

import "testing"

func TestNormalizeFiltersDeduplicatesAndSorts(t *testing.T) {
	got := normalize([]Process{
		{PID: 20, Executable: " Zoom.exe "},
		{PID: 0, Executable: "System"},
		{PID: 10, Executable: "chrome.exe"},
		{PID: 10, Executable: "duplicate.exe"},
		{PID: 30},
		{PID: 15, Executable: "Chrome.exe"},
	})
	want := []Process{
		{PID: 10, Executable: "chrome.exe"},
		{PID: 15, Executable: "Chrome.exe"},
		{PID: 20, Executable: "Zoom.exe"},
	}
	if len(got) != len(want) {
		t.Fatalf("normalize() = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("normalize()[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}
