package datadir

import (
	"os"
	"path/filepath"
	"testing"
)

func isolate(t *testing.T) (home string, working string) {
	t.Helper()
	home = t.TempDir()
	working = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("STEWARD_HOME", "")
	t.Chdir(working)
	return home, working
}

func writeDatabase(t *testing.T, directory string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "steward.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDefaultsToHomeDirectory(t *testing.T) {
	home, _ := isolate(t)
	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, Name); got.Path != want || got.WorkingDirectory {
		t.Fatalf("Resolve() = %+v, want %s", got, want)
	}
	database, err := DatabasePath()
	if err != nil || database != filepath.Join(home, Name, "steward.db") {
		t.Fatalf("DatabasePath() = %q, %v", database, err)
	}
}

func TestResolveHonorsStewardHome(t *testing.T) {
	_, working := isolate(t)
	writeDatabase(t, filepath.Join(working, Name))
	t.Setenv("STEWARD_HOME", "custom")
	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs("custom")
	if got.Path != want || got.WorkingDirectory {
		t.Fatalf("Resolve() = %+v, want %s", got, want)
	}
}

func TestResolveKeepsExistingWorkingDirectoryData(t *testing.T) {
	_, working := isolate(t)
	writeDatabase(t, filepath.Join(working, Name))
	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(Name)
	if got.Path != want || !got.WorkingDirectory {
		t.Fatalf("Resolve() = %+v, want working directory %s", got, want)
	}
}

func TestResolvePrefersHomeWhenBothHaveData(t *testing.T) {
	home, working := isolate(t)
	writeDatabase(t, filepath.Join(working, Name))
	writeDatabase(t, filepath.Join(home, Name))
	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, Name); got.Path != want || got.WorkingDirectory {
		t.Fatalf("Resolve() = %+v, want %s", got, want)
	}
}
