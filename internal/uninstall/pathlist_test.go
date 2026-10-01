package uninstall

import "testing"

func TestRemovePathEntry(t *testing.T) {
	dir := `C:\Users\dev\AppData\Local\Programs\airlock`
	tests := []struct {
		name  string
		list  string
		want  string
		found bool
	}{
		{"last entry", `C:\tools;` + dir, `C:\tools`, true},
		{"middle entry, other case and trailing slash", `C:\tools;C:\USERS\dev\AppData\Local\Programs\airlock\;%USERPROFILE%\bin`, `C:\tools;%USERPROFILE%\bin`, true},
		{"only entry", dir, ``, true},
		{"absent", `C:\tools;C:\Users\dev\go\bin`, `C:\tools;C:\Users\dev\go\bin`, false},
		{"empty", ``, ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := removePathEntry(tt.list, dir)
			if got != tt.want || found != tt.found {
				t.Fatalf("removePathEntry = %q, %v; want %q, %v", got, found, tt.want, tt.found)
			}
		})
	}
}
