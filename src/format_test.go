//go:build windows

package main

import (
	"os"
	"testing"
)

func TestFormatConfigCollapsesLeafEntries(t *testing.T) {
	path := writeConfig(t, `{"items":[{"label":"ExplorerPatcher","exec":"C:\\Windows\\System32\\rundll32.exe","args":["C:\\Program Files\\ExplorerPatcher\\ep_gui.dll","ZZGUI"],"icon":"%SystemRoot%\\System32\\shell32.dll,25"}]}`)

	if code := formatConfig(path); code != 0 {
		t.Fatalf("formatConfig returned %d", code)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"    \"items\": [\n" +
		"        { \"label\": \"ExplorerPatcher\", \"exec\": \"C:\\\\Windows\\\\System32\\\\rundll32.exe\", \"args\": [\"C:\\\\Program Files\\\\ExplorerPatcher\\\\ep_gui.dll\", \"ZZGUI\"], \"icon\": \"%SystemRoot%\\\\System32\\\\shell32.dll,25\" }\n" +
		"    ]\n" +
		"}\n"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// An entry with a nested "items" submenu must stay expanded -- collapsing it
// onto one line would make the submenu unreadable and defeats the purpose of
// formatting at all.
func TestFormatConfigExpandsSubmenus(t *testing.T) {
	path := writeConfig(t, `{"items":[{"label":"Sub","items":[{"label":"Child","exec":"a.exe"}]}]}`)

	if code := formatConfig(path); code != 0 {
		t.Fatalf("formatConfig returned %d", code)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"    \"items\": [\n" +
		"        {\n" +
		"            \"label\": \"Sub\",\n" +
		"            \"items\": [\n" +
		"                { \"label\": \"Child\", \"exec\": \"a.exe\" }\n" +
		"            ]\n" +
		"        }\n" +
		"    ]\n" +
		"}\n"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Key order is part of the source file, not something LoadConfig's struct
// tags get to decide -- formatting must not silently reorder fields.
func TestFormatConfigPreservesKeyOrder(t *testing.T) {
	path := writeConfig(t, `{"items":[{"exec":"a.exe","label":"A"}]}`)

	if code := formatConfig(path); code != 0 {
		t.Fatalf("formatConfig returned %d", code)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n    \"items\": [\n        { \"exec\": \"a.exe\", \"label\": \"A\" }\n    ]\n}\n"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Once formatted, a second pass must be a no-op -- otherwise every
// --config-format run after the first would keep touching the file's mtime
// for no reason.
func TestFormatConfigIsIdempotent(t *testing.T) {
	path := writeConfig(t, `{"items":[{"label":"A","exec":"a.exe"}]}`)

	if code := formatConfig(path); code != 0 {
		t.Fatalf("first formatConfig returned %d", code)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code := formatConfig(path); code != 0 {
		t.Fatalf("second formatConfig returned %d", code)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("formatting was not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestFormatConfigSyntaxError(t *testing.T) {
	path := writeConfig(t, `{"items":[`)
	if code := formatConfig(path); code == 0 {
		t.Fatal("expected a non-zero exit for invalid JSON")
	}
}
