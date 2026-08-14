// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"testing"
	"unsafe"

	"github.com/posit-dev/go-python-packaging/version"
)

// TestStructSizes logs the sizes of the core value types. It asserts nothing;
// it exists so a size regression is visible in -v output.
func TestStructSizes(t *testing.T) {
	t.Logf("version.Version = %d bytes", unsafe.Sizeof(version.Version{}))
	t.Logf("posKey          = %d bytes", unsafe.Sizeof(posKey{}))
	t.Logf("bound           = %d bytes", unsafe.Sizeof(bound{}))
	t.Logf("span            = %d bytes", unsafe.Sizeof(span{}))
	t.Logf("Set             = %d bytes", unsafe.Sizeof(Set{}))
}
