// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"fmt"
	"strings"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/tags"
)

// maxReportedTags bounds how many of a version's tags a rejection quotes. A
// six-platform release can publish dozens, and a failure report that pastes all
// of them stops being read.
const maxReportedTags = 6

// WheelTagMatcher decides whether a wheel can run on the resolution's target.
//
// An interface rather than *tags.Matcher so the policy is the caller's: the same
// target compiled once answers IsCompatible, and a caller that wants
// go-python-packaging's IsCompatibleOrNewer semantics -- admit a wheel built for
// an OLDER platform baseline than the target -- wraps the same matcher in three
// lines instead of waiting for this package to grow a mode flag.
type WheelTagMatcher interface {
	// IsCompatible reports whether any of the given tags runs on the target. The
	// argument is a slice because one tag string can expand to several tags:
	// "cp39.cp310-none-any" is two.
	IsCompatible(w []tags.Tag) bool
}

// WheelTagFilter turns on rejection of versions whose wheels cannot run on the
// resolution's target. A nil *WheelTagFilter in Options leaves tag filtering off.
//
// ⚠️ Compile ONE per environment cell and do not share a filter across cells. The
// matcher is the whole of what makes a tag "compatible", so a filter reused for a
// second target answers the first target's question under the second target's
// name -- silently, since every answer is still a plausible one.
type WheelTagFilter struct {
	// Matcher is the compiled target. Required; a WheelTagFilter with no matcher
	// filters nothing, on the same fail-open principle as an incomplete index.
	Matcher WheelTagMatcher

	// Target names the environment for the failure report -- "cp311 on
	// manylinux_2_28_x86_64", or whatever the caller called it.
	//
	// It exists because a rejection that does not name what it was matching
	// against is unactionable: "no compatible wheel" leaves the reader unable to
	// tell whether their target or the package is the surprise. Empty renders as
	// "the target environment", which is worse but not wrong.
	Target string
}

// target returns the human-readable target name, never empty.
func (f *WheelTagFilter) target() string {
	if f == nil || f.Target == "" {
		return "the target environment"
	}
	return f.Target
}

// tagFilteringEnabled reports whether this resolution may reject a version for
// its wheel tags.
//
// ⚠️ BOTH conditions are load-bearing and the index one is the easy one to drop.
// A caller asking for tag filtering is not sufficient: if the index's tag data is
// incomplete, a version with no matching tag is indistinguishable from one whose
// tags were never derived, so filtering rejects packages on the strength of
// missing data. As of 2026-09 the production PyPI snapshot is exactly that --
// tags for 1.2% of packages -- so this returns false in production today, and
// that is the correct behaviour rather than a stub.
func tagFilteringEnabled(idx index.MetadataIndex, filter *WheelTagFilter) bool {
	if filter == nil || filter.Matcher == nil {
		return false
	}
	return index.WheelTagsComplete(idx)
}

// rejectedForWheelTags reports whether this version must not be offered because
// nothing it publishes can be installed on the target.
//
// # The rule, and why four of the five cases are "usable"
//
//	a compatible wheel                 -> usable
//	wheels, none compatible, an sdist  -> usable; RFD 0001 §5.3 falls back to source
//	wheels, none compatible, no sdist  -> REJECT
//	no wheels, an sdist                -> usable
//	no files at all                    -> REJECT
//	tags never captured                -> usable; nothing is known, so nothing is claimed
//
// Rejecting only when there is nothing left to install is what keeps this filter
// from narrowing a resolution that would have succeeded. A version whose wheels
// all miss the target is still buildable from its sdist, and a resolver that
// refused it would be substituting its own preference for the user's.
//
// An unreadable tag counts as not compatible, which is also what IsCompatible
// answers for a tag it does not recognize -- so a tag vocabulary that grows a
// spelling this build cannot parse loses that wheel rather than the version.
func (p *Provider) rejectedForWheelTags(meta index.PackageMetadata) (reason string, rejected bool) {
	if !p.tagFilter {
		return "", false
	}
	// Nothing was derived for this version, so there is no claim to test. This is
	// not the same as an empty tag list, and conflating the two rejects a version
	// on the strength of data that was never collected.
	if !meta.TagsCaptured {
		return "", false
	}

	for _, raw := range meta.WheelTags {
		parsed, err := tags.ParseTag(raw)
		if err != nil {
			continue
		}
		if p.opts.WheelTags.Matcher.IsCompatible(parsed) {
			return "", false
		}
	}

	// No compatible wheel. An sdist is still a way in.
	if meta.HasSdist {
		return "", false
	}

	target := p.opts.WheelTags.target()
	if len(meta.WheelTags) == 0 {
		return "it publishes no wheels and no source distribution, so there is nothing " +
			"to install for " + target, true
	}
	return fmt.Sprintf(
		"none of its wheels can run on %s (it publishes %s) and it has no source "+
			"distribution to build from",
		target, summarizeTags(meta.WheelTags)), true
}

// summarizeTags renders a version's tags for a failure report, bounded.
func summarizeTags(wheelTags []string) string {
	if len(wheelTags) <= maxReportedTags {
		return strings.Join(wheelTags, ", ")
	}
	shown := strings.Join(wheelTags[:maxReportedTags], ", ")
	return fmt.Sprintf("%s and %d more", shown, len(wheelTags)-maxReportedTags)
}
