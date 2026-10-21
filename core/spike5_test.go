package core

import "testing"

func TestSpike5Paths(t *testing.T) {
	for _, paths := range [][]string{
		{"/.spike5/view/a", "/.spike5/view/b"},
		{"/.spike5/view/nested/a", "/.spike5/view/nested/b"},
	} {
		if err := spike5ValidatePaths(paths); err != nil {
			t.Fatal(err)
		}
	}
	for _, paths := range [][]string{
		{"/etc/passwd"}, {"relative"}, {"/.spike5/view/../escape"},
		{"/.spike5/view/a", "/.spike5/view/a"},
		{"/.spike5/view/a", "/.spike5/view/a/b"},
		{"/.spike5/view/a/b", "/.spike5/view/a"},
	} {
		if err := spike5ValidatePaths(paths); err == nil {
			t.Fatalf("accepted %v", paths)
		}
	}
}
