package backend

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestParseVersionOutput(t *testing.T) {
	cases := map[string]string{
		"container CLI version 1.3.0 (build: release, commit: d6de569)\n": "1.3.0",
		"openshell-gateway 0.0.116\n":                                     "0.0.116",
		"driver v0.3.0 (commit abc)\n":                                    "0.3.0",
		"weird output\n":                                                  "",
		"":                                                                "",
		"1.2\n":                                                           "",
	}
	for in, want := range cases {
		if got := ParseVersionOutput(in); got != want {
			t.Errorf("ParseVersionOutput(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCLIStopStartVersionArgv(t *testing.T) {
	rr := &recordingRunner{stdout: []byte("container CLI version 1.4.1 (build: release, commit: abc)\n")}
	c := &CLI{Bin: "container", Runner: rr}
	ctx := context.Background()
	if err := c.Stop(ctx, "oshl-x"); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, "oshl-x"); err != nil {
		t.Fatal(err)
	}
	v, err := c.Version(ctx)
	if err != nil || v != "1.4.1" {
		t.Errorf("Version = %q, %v", v, err)
	}
	want := [][]string{
		{"container", "stop", "--time", stopGraceSeconds, "oshl-x"},
		{"container", "start", "oshl-x"},
		{"container", "--version"},
	}
	if !reflect.DeepEqual(rr.calls, want) {
		t.Errorf("argv = %v, want %v", rr.calls, want)
	}
}

func TestCLIStopStartNotFound(t *testing.T) {
	rr := &recordingRunner{stderr: []byte("Error: container not found: oshl-x"), err: errors.New("exit status 1")}
	c := &CLI{Bin: "container", Runner: rr}
	if err := c.Stop(context.Background(), "oshl-x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("stop: want ErrNotFound, got %v", err)
	}
	if err := c.Start(context.Background(), "oshl-x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("start: want ErrNotFound, got %v", err)
	}
}
