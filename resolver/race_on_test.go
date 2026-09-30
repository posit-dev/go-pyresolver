// SPDX-License-Identifier: Apache-2.0 OR MIT

//go:build race

package resolver_test

// raceEnabled lets a slow test shrink under the race detector.
const raceEnabled = true
