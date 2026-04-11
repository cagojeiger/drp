// Package compat contains the drps ↔ frps functional-equivalence test suite.
//
// This package is separate from the existing E2E suite at test/integration_test.go
// (package test) which MUST remain byte-identical. The compat suite adds scenario-
// driven equivalence verification on top of the same testcontainers-go + DinD
// infrastructure.
//
// See .omc/plans/compat-tests-plan.md and .omc/specs/deep-interview-compat-tests.md
// for the design rationale and consensus-approved implementation plan.
package compat
