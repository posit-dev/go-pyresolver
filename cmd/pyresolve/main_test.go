// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunNoArgsPrintsHelpAndExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("expected usage text on stderr, got:\n%s", stderr.String())
	}
}

func TestRunHelpExitsZeroOnStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "never makes a network request") {
		t.Errorf("top-level help must state the no-network guarantee, got:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("help should not write to stderr, got:\n%s", stderr.String())
	}
}

func TestRunUnknownCommandExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"frobnicate"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), `"frobnicate"`) {
		t.Errorf("expected the unknown command to be named, got:\n%s", stderr.String())
	}
}

func TestRunStatsEndToEndAgainstFixture(t *testing.T) {
	path := standardFixture(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"stats", "--rsf", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Packages:") {
		t.Errorf("expected stats output, got:\n%s", stdout.String())
	}
}

func TestRunMissingRSFFlagIsUsageError(t *testing.T) {
	t.Setenv("PYRESOLVE_RSF", "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"stats"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "--rsf") {
		t.Errorf("expected the error to mention --rsf, got:\n%s", stderr.String())
	}
}

func TestRunHonorsPYRESOLVE_RSFEnvVar(t *testing.T) {
	path := standardFixture(t)
	t.Setenv("PYRESOLVE_RSF", path)

	var stdout, stderr bytes.Buffer
	code := run([]string{"stats"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
}

func TestRunSubcommandHelpTexts(t *testing.T) {
	for _, cmd := range []string{"stats", "versions", "deps", "walk"} {
		t.Run(cmd, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{cmd, "--help"}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "network request") {
				t.Errorf("%s --help must state the no-network guarantee, got:\n%s", cmd, stdout.String())
			}
		})
	}
}

func TestRunWalkHelpStatesItIsNotAResolver(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"walk", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "NOT dependency resolution") {
		t.Errorf("walk --help must plainly state it does not resolve, got:\n%s", stdout.String())
	}
}

func TestRunPackageNotFoundExitsTwo(t *testing.T) {
	path := standardFixture(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"versions", "--rsf", path, "no-such-package"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// TestRunNeverImportsNetHTTP pins the "never makes a network request"
// guarantee at the source level: no .go file in this package may import
// net/http, since that would be the only way this CLI could reach the
// network.
func TestRunNeverImportsNetHTTP(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}
	for _, e := range entries {
		// Only the non-test sources matter: they are what ships in the
		// binary. Skipping _test.go also avoids this very check tripping
		// over its own string literal below.
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		if strings.Contains(string(src), `"net/http"`) {
			t.Errorf("%s imports net/http; this CLI must never make a network request", e.Name())
		}
	}
}
