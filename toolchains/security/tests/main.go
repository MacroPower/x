// Package main implements fixture-based tests for the security toolchain
// module.
//
// Gate scans run against a synthetic, dependency-free fixture (see
// testdata/fixture) for the pass case and against a known end-of-life image
// for the fail case. SARIF scans assert structurally valid Trivy output.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"dagger/tests/internal/dagger"
)

// Tests exercises the security toolchain.
type Tests struct{}

// cleanSource returns the dependency-free fixture used for the passing gate
// scan.
func (t *Tests) cleanSource() *dagger.Directory {
	return dag.CurrentModule().Source().Directory("testdata/fixture")
}

// subject constructs the security module under test with the clean fixture.
func (t *Tests) subject() *dagger.Security {
	return dag.Security(dagger.SecurityOpts{Source: t.cleanSource()})
}

// All runs every test in sequence and reports the first failure.
func (t *Tests) All(ctx context.Context) error {
	cases := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"scan-source-clean", t.ScanSourceClean},
		{"scan-source-sarif", t.ScanSourceSarif},
		{"scan-image-gate", t.ScanImageGate},
		{"scan-image-sarif", t.ScanImageSarif},
	}
	for _, tc := range cases {
		if err := tc.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
	}
	return nil
}

// ScanSourceClean verifies a dependency-free source tree passes the gate scan
// (exit code 0, no error).
func (t *Tests) ScanSourceClean(ctx context.Context) error {
	return t.subject().ScanSource(ctx)
}

// ScanImageGate verifies the gating image scan fails (returns an error) on a
// known end-of-life image, proving --exit-code=1 is honored.
func (t *Tests) ScanImageGate(ctx context.Context) error {
	target := dag.Container().From("public.ecr.aws/docker/library/alpine:3.10")
	if err := t.subject().ScanImage(ctx, target); err == nil {
		return fmt.Errorf("expected ScanImage gate to fail on a vulnerable image")
	}
	return nil
}

// ScanSourceSarif verifies the SARIF source scan produces structurally valid
// Trivy output.
func (t *Tests) ScanSourceSarif(ctx context.Context) error {
	contents, err := t.subject().ScanSourceSarif().Contents(ctx)
	if err != nil {
		return err
	}
	return assertTrivySarif(contents)
}

// ScanImageSarif verifies the SARIF image scan produces structurally valid
// Trivy output without failing on findings (it omits --exit-code=1).
func (t *Tests) ScanImageSarif(ctx context.Context) error {
	target := dag.Container().From("public.ecr.aws/docker/library/alpine:3.10")
	contents, err := t.subject().ScanImageSarif(target).Contents(ctx)
	if err != nil {
		return err
	}
	return assertTrivySarif(contents)
}

// assertTrivySarif checks that contents is a well-formed SARIF document
// produced by Trivy, not merely a non-empty file.
func assertTrivySarif(contents string) error {
	var doc struct {
		Runs []struct {
			Tool struct {
				Driver struct {
					Name string `json:"name"`
				} `json:"driver"`
			} `json:"tool"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(contents), &doc); err != nil {
		return fmt.Errorf("SARIF is not valid JSON: %w", err)
	}
	if len(doc.Runs) == 0 {
		return fmt.Errorf("SARIF has no runs")
	}
	if got := doc.Runs[0].Tool.Driver.Name; got != "Trivy" {
		return fmt.Errorf("SARIF tool driver = %q, want %q", got, "Trivy")
	}
	return nil
}
