package main

import (
	"strings"
	"testing"
)

func TestRewriteLegacyArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"no args defaults to up", nil, []string{"up"}},
		{"empty args defaults to up", []string{}, []string{"up"}},
		{"known subcommand passes through", []string{"down", "/tmp"}, []string{"down", "/tmp"}},
		{"bare path passes through unchanged", []string{"/home/user/project"}, []string{"/home/user/project"}},
		{"unknown flag passes through unchanged", []string{"--verbose"}, []string{"--verbose"}},
		{"-h becomes help", []string{"-h"}, []string{"help"}},
		{"--help becomes help", []string{"--help"}, []string{"help"}},
		{"--help with extra args", []string{"--help", "foo"}, []string{"help", "foo"}},
		{"-V becomes --version", []string{"-V"}, []string{"--version"}},
		{"--version passes through", []string{"--version"}, []string{"--version"}},
		{"--clean becomes clean", []string{"--clean"}, []string{"clean"}},
		{"--clean with args", []string{"--clean", "/tmp"}, []string{"clean", "/tmp"}},
		{"--rebuild becomes up --rebuild", []string{"--rebuild"}, []string{"up", "--rebuild"}},
		{"--rebuild with args", []string{"--rebuild", "/tmp"}, []string{"up", "--rebuild", "/tmp"}},
		{"-p flag passes through unchanged", []string{"-p", "3000:3000"}, []string{"-p", "3000:3000"}},
		{"up passes through", []string{"up"}, []string{"up"}},
		{"up with flags", []string{"up", "-p", "8080:8080", "/tmp"}, []string{"up", "-p", "8080:8080", "/tmp"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteLegacyArgs(tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("rewriteLegacyArgs(%v) = %v, want %v", tt.args, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("rewriteLegacyArgs(%v) = %v, want %v", tt.args, got, tt.want)
				}
			}
		})
	}
}

func TestUnknownCommandHint(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantOK  bool
		wantSub string // substring expected in the hint detail when wantOK
	}{
		{"bare path is hinted toward up", []string{"/home/user/project"}, true, "devc up /home/user/project"},
		{"bare name is hinted toward up", []string{"myproject"}, true, "devc up myproject"},
		{"known subcommand is not hinted", []string{"up"}, false, ""},
		{"down subcommand is not hinted", []string{"down", "/tmp"}, false, ""},
		{"flag is not hinted", []string{"--verbose"}, false, ""},
		{"short flag is not hinted", []string{"-p", "3000:3000"}, false, ""},
		{"no args is not hinted", nil, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail, ok := unknownCommandHint(tt.args)
			if ok != tt.wantOK {
				t.Fatalf("unknownCommandHint(%v) ok = %v, want %v", tt.args, ok, tt.wantOK)
			}
			if ok && !strings.Contains(detail, tt.wantSub) {
				t.Fatalf("unknownCommandHint(%v) detail = %q, want it to contain %q", tt.args, detail, tt.wantSub)
			}
		})
	}
}
