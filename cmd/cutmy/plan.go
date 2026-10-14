package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

// Both checking and executing use the same bounded, strict portable plan.
func readCuttingPlan(path string) (plan engine.Plan, err error) {
	file, err := os.Open(path)
	if err != nil {
		return plan, err
	}
	defer file.Close()
	const maxPlanBytes = 1 << 20
	blob, err := io.ReadAll(io.LimitReader(file, maxPlanBytes+1))
	if err != nil {
		return plan, err
	}
	if len(blob) > maxPlanBytes {
		return plan, invalidCLI(errors.New("plan exceeds 1 MiB; use at most 128 named intervals"))
	}
	decoder := json.NewDecoder(bytes.NewReader(blob))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&plan); err != nil {
		return plan, invalidCLI(fmt.Errorf("invalid cutting plan: %w", err))
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return plan, invalidCLI(errors.New("plan must contain exactly one JSON object"))
	}
	if plan.Input != "" && !filepath.IsAbs(plan.Input) {
		plan.Input = filepath.Join(filepath.Dir(path), plan.Input)
	}
	return plan, nil
}
