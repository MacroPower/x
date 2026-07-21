// Package constraint is the shared typed constraint algebra for schema
// generation. The three constraint sources -- Go-kind reflection, the
// jsonschema struct tag, and tag interpreters such as the validate tag -- each
// express their bounds and values as typed constraints and merge them through
// this one model. Intersection, conflict detection, unsatisfiability, and the
// const/enum-subsumes-bounds precedence are each defined once here and are
// order-independent: precedence is data (a contribution [Mode] and [Provenance])
// rather than the order the sources happen to run in.
//
// The model has three parts:
//
//   - [Interval] over exact decimals ([math/big.Rat] endpoints) for the numeric
//     bounds and, in an integer non-negative domain, the length/count ranges.
//     [Interval.Intersect] is the single keep-tighter combinator for the
//     floor/ceiling merge.
//   - [ValueSet] for const/enum (allowed) and ne/not (forbidden) values, with
//     the forbidden-value escalation (not.const -> not.enum -> allOf) and one
//     numeric-aware equality built on exact rationals.
//   - [Set], the per-node aggregate. [Set.Resolve] applies the value-set-driven
//     const/enum precedence and surfaces a conflict once, while
//     [Set.ResolveBounds] resolves under a caller-supplied [ResolveMode] so the
//     reconcile path, which alone knows whether a node is a field or an element,
//     drives the numeric-bound subsumption itself.
//
// The package renders onto the upstream
// github.com/google/jsonschema-go/jsonschema.Schema type (which the main package
// aliases), not the main package's alias, so both the main-package reflection
// pipeline and the sibling tag-interpreter packages can import it without a
// cycle.
package constraint
