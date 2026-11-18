// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"time"

	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
)

// CountSolves counts the solves Resolve runs until the returned func is called.
// Not safe with parallel tests.
func CountSolves() (count func() int, restore func()) {
	n := 0
	onSolve = func() { n++ }
	return func() int { return n }, func() { onSolve = nil }
}

// CountSpecTests counts the root specifier tests the upgrade pass runs until
// the returned func is called. Not safe with parallel tests.
func CountSpecTests() (count func() int, restore func()) {
	n := 0
	onSpecTest = func() { n++ }
	return func() int { return n }, func() { onSpecTest = nil }
}

// OnSolve calls f before each solve until restore is called.
// Not safe with parallel tests.
func OnSolve(f func()) (restore func()) {
	onSolve = f
	return func() { onSolve = nil }
}

// OnSolver calls f with each configured Solver until restore is called.
// Not safe with parallel tests.
func OnSolver(f func(*solver.Solver[provider.Package, pep440set.Set])) (restore func()) {
	onSolver = f
	return func() { onSolver = nil }
}

// SetClock replaces the clock the upgrade time bound reads until restore is
// called. Not safe with parallel tests.
func SetClock(f func() time.Time) (restore func()) {
	now = f
	return func() { now = time.Now }
}

// RecordSteps records the solver steps of each resolution Resolve runs, the
// first solve then each upgrade re-solve, until restore is called.
// Not safe with parallel tests.
func RecordSteps() (got func() []int, restore func()) {
	var all []int
	onSteps = func(n int) { all = append(all, n) }
	return func() []int { return all }, func() { onSteps = nil }
}

// OnCandidate calls f with each answer an upgrade re-solve found, and whether
// the pass kept it, until restore is called. Not safe with parallel tests.
func OnCandidate(f func(got *Resolution, kept bool)) (restore func()) {
	onCandidate = f
	return func() { onCandidate = nil }
}
