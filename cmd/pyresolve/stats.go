// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

const statsHelp = `Usage: pyresolve stats --rsf <path> [--json]

Print the package count, the dependency dictionary's name count, and the
file size of an RSF file.

Flags:
  --rsf <path>   path to the RSF file (or set PYRESOLVE_RSF)
  --json         emit JSON instead of text

` + noNetworkNotice + "\n"

// statsResult is the stats command's output shape, shared by text and JSON.
type statsResult struct {
	Path        string `json:"path"`
	Packages    int    `json:"packages"`
	DictNames   int    `json:"dict_names"`
	FileSizeB   int64  `json:"file_size_bytes"`
	FileSizeStr string `json:"file_size_human"`
}

// runStats parses args and flags, then delegates to statsCmd.
func runStats(w io.Writer, args []string) error {
	fs, g := newFlagSet("stats")
	fs.Usage = func() {}
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, printErr := fmt.Fprint(w, statsHelp)
			return printErr
		}
		return usageErrorf("stats: %v", err)
	}
	if fs.NArg() != 0 {
		return usageErrorf("stats: unexpected argument(s) %v; stats takes no positional arguments", fs.Args())
	}

	path, err := resolveRSFPath(g.rsfPath)
	if err != nil {
		return err
	}

	return statsCmd(w, path, g.json)
}

// statsCmd is the testable core: it opens the RSF at path, gathers the
// counts, and writes the result to w.
func statsCmd(w io.Writer, path string, jsonOut bool) error {
	file, _, err := openRSF(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	info, err := os.Stat(path)
	if err != nil {
		return usageErrorf("stats: statting %s: %v", path, err)
	}

	result := statsResult{
		Path:        path,
		Packages:    file.Len(),
		DictNames:   len(file.Dict().Names()),
		FileSizeB:   info.Size(),
		FileSizeStr: humanBytes(info.Size()),
	}

	if jsonOut {
		return writeJSON(w, result)
	}

	ew := &errWriter{w: w}
	ew.printf("RSF file:              %s\n", result.Path)
	ew.printf("Packages:               %d\n", result.Packages)
	ew.printf("Dependency dict names:  %d\n", result.DictNames)
	ew.printf("File size:              %d bytes (%s)\n", result.FileSizeB, result.FileSizeStr)
	return ew.err
}

// humanBytes renders n as a human-friendly size, 1024-based.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	units := "KMGTPE"
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), units[exp])
}
