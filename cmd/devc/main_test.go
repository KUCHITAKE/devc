package main

import (
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
		{"bare path becomes up <path>", []string{"/home/user/project"}, []string{"up", "/home/user/project"}},
		{"unknown flag becomes up flag", []string{"--verbose"}, []string{"up", "--verbose"}},
		{"-h becomes help", []string{"-h"}, []string{"help"}},
		{"--help becomes help", []string{"--help"}, []string{"help"}},
		{"--help with extra args", []string{"--help", "foo"}, []string{"help", "foo"}},
		{"-V becomes --version", []string{"-V"}, []string{"--version"}},
		{"--version passes through", []string{"--version"}, []string{"--version"}},
		{"--clean becomes clean", []string{"--clean"}, []string{"clean"}},
		{"--clean with args", []string{"--clean", "/tmp"}, []string{"clean", "/tmp"}},
		{"--rebuild becomes up --rebuild", []string{"--rebuild"}, []string{"up", "--rebuild"}},
		{"--rebuild with args", []string{"--rebuild", "/tmp"}, []string{"up", "--rebuild", "/tmp"}},
		{"-p flag becomes up -p", []string{"-p", "3000:3000"}, []string{"up", "-p", "3000:3000"}},
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
