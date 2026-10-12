package cloudflare

import "testing"

func TestExpandNamesTemplates(t *testing.T) {
	got, err := ExpandNames("node-{n:3}", "Example.COM.", 3)
	if err != nil {
		t.Fatalf("ExpandNames() error = %v", err)
	}
	want := []string{"node-001.example.com", "node-002.example.com", "node-003.example.com"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ExpandNames()[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestExpandNamesRejectsInvalidLabels(t *testing.T) {
	if _, err := ExpandNames("bad label", "example.com", 1); err == nil {
		t.Fatal("ExpandNames() accepted an invalid label")
	}
	if _, err := ExpandNames("node-{n}", "", 1); err == nil {
		t.Fatal("ExpandNames() accepted an empty zone")
	}
}
