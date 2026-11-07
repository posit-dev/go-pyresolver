// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"fmt"

	"github.com/posit-dev/go-pyresolver/pypirsf"
	"github.com/posit-dev/go-python-packaging/version"
)

// KnownNoWheels reports whether the stored record for pkg ver captured its wheel
// tags and lists none, without parsing the record's requirements. It reads the
// same fields Metadata would return as TagsCaptured and WheelTags.
//
// Errors follow Metadata: ErrMetadataUnavailable when the version is not stored.
// A record Metadata would call unusable is NOT detected here, since that needs
// the requirement parse this exists to skip; callers that must treat it
// differently confirm a true answer with Metadata.
func (idx *RSFIndex) KnownNoWheels(ctx context.Context, pkg PackageName, ver version.Version) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := checkVersionInitialized("KnownNoWheels", pkg, ver); err != nil {
		return false, err
	}
	decoded, err := idx.deps(pkg)
	if err != nil {
		return false, err
	}
	key, found, err := idx.resolveStoredKey(pkg, ver, decoded)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("index %q: %q %s: %w", idx.origin, pkg, ver, ErrMetadataUnavailable)
	}
	return noWheels(decoded[key]), nil
}

// noWheels is the one predicate shared with the full-decode path's test.
func noWheels(raw pypirsf.VersionDeps) bool {
	return raw.TagsCaptured && len(raw.WheelTags) == 0
}
