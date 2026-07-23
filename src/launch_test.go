//go:build windows

package main

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

var (
	procCommandLineToArgvW = shell32.NewProc("CommandLineToArgvW")
	procLocalFree          = kernel32.NewProc("LocalFree")
)

// commandLineToArgv runs a parameter string through the same parser the C
// runtime and most applications use, so the quoting rules are verified against
// Windows itself rather than against my reading of the documentation.
//
// A program name is prepended and dropped again because argv[0] follows
// different, looser quoting rules -- and ShellExecuteEx's lpParameters never
// contains argv[0] anyway.
func commandLineToArgv(params string) ([]string, error) {
	line := "prog.exe"
	if params != "" {
		line += " " + params
	}
	p, err := syscall.UTF16PtrFromString(line)
	if err != nil {
		return nil, err
	}
	var argc int32
	r, _, e := procCommandLineToArgvW.Call(uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&argc)))
	runtime.KeepAlive(p)
	if r == 0 {
		return nil, e
	}
	defer procLocalFree.Call(r)

	argv := unsafe.Slice(winPtr[*uint16](r), int(argc))
	out := make([]string, 0, argc)
	for _, s := range argv {
		out = append(out, utf16PtrToString(s))
	}
	return out[1:], nil
}

// The array form of "args" in config.json exists precisely so that quoting is
// not the user's problem. These cases are the CommandLineToArgvW rules: a
// backslash is only special immediately before a quote.
func TestQuoteArg(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`-profile`, `-profile`},
		{`simple`, `simple`},
		{``, `""`}, // an empty argument must survive as an empty argument
		{`has space`, `"has space"`},
		{`C:\Program Files\Mozilla Firefox\firefox.exe`, `"C:\Program Files\Mozilla Firefox\firefox.exe"`},
		{`C:\Data\Profiles\x`, `C:\Data\Profiles\x`}, // no space: no quoting needed
		{`C:\my dir\`, `"C:\my dir\\"`},              // trailing slash doubled inside quotes
		{`a"b`, `"a\"b"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\\slash`, `back\\slash`},
		{`in a\\dir`, `"in a\\dir"`}, // slashes not before a quote stay as-is
		{"tab\there", "\"tab\there\""},
	}
	for _, c := range cases {
		if got := quoteArg(c.in); got != c.want {
			t.Errorf("quoteArg(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

// A Store app is launched through the AppsFolder moniker built from its
// AppUserModelID; a plain entry is launched by its exec path unchanged.
func TestLaunchTarget(t *testing.T) {
	if got := launchTarget(&node{exec: `C:\Windows\notepad.exe`}); got != `C:\Windows\notepad.exe` {
		t.Errorf("exec target = %q", got)
	}
	got := launchTarget(&node{appID: "Microsoft.Windows.Photos_8wekyb3d8bbwe!App"})
	want := `shell:AppsFolder\Microsoft.Windows.Photos_8wekyb3d8bbwe!App`
	if got != want {
		t.Errorf("appId target = %q, want %q", got, want)
	}
}

func TestQuoteArgs(t *testing.T) {
	got := quoteArgs([]string{"-profile", `C:\Data\Profiles\firefox secondary1`, "-no-remote"})
	want := `-profile "C:\Data\Profiles\firefox secondary1" -no-remote`
	if got != want {
		t.Errorf("quoteArgs\n got  %q\n want %q", got, want)
	}
	if quoteArgs(nil) != "" {
		t.Errorf("quoteArgs(nil) should be empty")
	}
}

// A round trip through the documented escaping rules: whatever quoteArg
// produces, CommandLineToArgvW must split back into the original strings.
func TestQuoteArgsRoundTrip(t *testing.T) {
	inputs := [][]string{
		{`plain`},
		{`with space`, `other`},
		{`C:\Program Files\App\app.exe`, `--flag=a b`},
		{`quote"inside`},
		{`trailing\`, `slash\\`},
		{`C:\dir with space\`},
		{``, `after empty`},
	}
	for _, args := range inputs {
		line := quoteArgs(args)
		got, err := commandLineToArgv(line)
		if err != nil {
			t.Fatalf("CommandLineToArgvW(%q): %v", line, err)
		}
		if len(got) != len(args) {
			t.Errorf("%q -> %q: got %d args, want %d", args, line, len(got), len(args))
			continue
		}
		for i := range args {
			if got[i] != args[i] {
				t.Errorf("%q -> %q: arg %d = %q, want %q", args, line, i, got[i], args[i])
			}
		}
	}
}
