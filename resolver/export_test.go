// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

// CountSolves counts the solves Resolve runs until the returned func is called.
// Not safe with parallel tests.
func CountSolves() (count func() int, restore func()) {
	n := 0
	onSolve = func() { n++ }
	return func() int { return n }, func() { onSolve = nil }
}
