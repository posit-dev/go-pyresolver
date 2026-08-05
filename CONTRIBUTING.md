# Contributing to go-pyresolver

## Attribution belongs in the same commit as the material

If a change adapts, ports, or is closely derived from another project's source, add or update
the corresponding paragraph in `NOTICE` **in that same commit** — not as a follow-up.

This is easy to get wrong in one specific way: appending a new paragraph at the end of `NOTICE`
without re-reading the top. A general statement made earlier in the file can be silently
contradicted by material added later. When you add an attribution, check whether anything above
it has become inaccurate.

For the same reason, `NOTICE` states what **is** incorporated and does not assert what is not.
A negative claim cannot be kept accurate as the code grows, and stating one creates a false
record the moment it stops being true.

## What `NOTICE` is for

It is a legal attribution file that ships with the software. It carries copyright notices,
license identifications, and statements of what material came from where. It is not a place for
maintenance notes, TODOs, or rationale — those belong here, in code comments, or in the issue
tracker.

## Reference implementations

This module implements published Python packaging standards (PEP 440, PEP 503, PEP 508, PEP 685
and related). Where behavior is ambiguous, the specifications govern, and `pypa/packaging` and
`pip` are the practical references for what correct behavior looks like.

Two consequences worth knowing:

- **Prefer the specification over any implementation when they disagree.** Reference
  implementations do carry deviations from the specs they implement, and we have chosen the
  spec's behavior over an upstream's on at least one occasion.
- **Verify against the reference rather than against our own tests.** A test written from the
  same misunderstanding as the code will agree with it. When adding behavior, check it against
  what `pypa/packaging` or `pip` actually does, and record the measured value in the test.
