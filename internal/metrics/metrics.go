// Package metrics defines the generic description of a metric.
//
// Metrics are data, not schema: a new benchmark or catalog attribute is a new
// Definition emitted by a collector, stored as a row, and queried through the
// same code paths as every other metric. Nothing in storage or querying knows
// any particular metric by name.
package metrics

import (
	"fmt"
	"regexp"
)

// ValueKind is the type of a metric's observed values.
type ValueKind string

const (
	KindNumber  ValueKind = "number"
	KindInteger ValueKind = "integer"
	KindBoolean ValueKind = "boolean"
	KindText    ValueKind = "text"
)

// Numeric reports whether values of this kind support ordering comparisons.
func (k ValueKind) Numeric() bool { return k == KindNumber || k == KindInteger }

// Direction states which way a metric is better, if either.
type Direction string

const (
	HigherIsBetter Direction = "higher_is_better"
	LowerIsBetter  Direction = "lower_is_better"
	Neutral        Direction = "neutral"
)

// Scope states how observations of a metric attach to catalog models.
type Scope string

const (
	// ScopeCatalog observations are published per catalog row by the Devin
	// dataset itself (prices, recommendation flags). They are bound to the
	// row's stable model id and belong to the selected dataset.
	ScopeCatalog Scope = "catalog"

	// ScopeEvidence observations are published by an external source about a
	// model identity, and are matched to catalog models through the identity
	// layer. They are independent of the selected dataset.
	ScopeEvidence Scope = "evidence"
)

// Status is a metric's lifecycle state.
type Status string

const (
	StatusCurrent    Status = "current"
	StatusDeprecated Status = "deprecated"
)

// Definition describes one metric.
type Definition struct {
	Key         string    `json:"key"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	ValueKind   ValueKind `json:"value_kind"`
	Unit        string    `json:"unit,omitempty"`
	Direction   Direction `json:"direction"`
	Scope       Scope     `json:"scope"`

	// DefinedBy is the source code that introduced the metric. Other sources
	// may still contribute observations of it.
	DefinedBy string `json:"defined_by"`

	// MissingPossible reports whether a catalog model may lack an observation.
	// It is true for nearly every metric; it is informational for agents.
	MissingPossible bool   `json:"missing_possible"`
	Status          Status `json:"status"`
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// ValidKey reports whether s is an acceptable metric key.
func ValidKey(s string) bool { return keyPattern.MatchString(s) }

// Validate checks that a definition is internally consistent.
func (d Definition) Validate() error {
	if !ValidKey(d.Key) {
		return fmt.Errorf("metric key %q must match %s", d.Key, keyPattern)
	}
	if d.DisplayName == "" || d.Description == "" {
		return fmt.Errorf("metric %s needs a display name and a description", d.Key)
	}
	switch d.ValueKind {
	case KindNumber, KindInteger, KindBoolean, KindText:
	default:
		return fmt.Errorf("metric %s has unknown value kind %q", d.Key, d.ValueKind)
	}
	switch d.Direction {
	case HigherIsBetter, LowerIsBetter, Neutral:
	default:
		return fmt.Errorf("metric %s has unknown direction %q", d.Key, d.Direction)
	}
	if !d.ValueKind.Numeric() && d.Direction != Neutral {
		return fmt.Errorf("metric %s is %s and so cannot have direction %s", d.Key, d.ValueKind, d.Direction)
	}
	switch d.Scope {
	case ScopeCatalog, ScopeEvidence:
	default:
		return fmt.Errorf("metric %s has unknown scope %q", d.Key, d.Scope)
	}
	switch d.Status {
	case StatusCurrent, StatusDeprecated:
	default:
		return fmt.Errorf("metric %s has unknown status %q", d.Key, d.Status)
	}
	if d.DefinedBy == "" {
		return fmt.Errorf("metric %s has no defining source", d.Key)
	}
	return nil
}
