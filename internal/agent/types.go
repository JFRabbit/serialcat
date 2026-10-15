// Package agent implements the serial port agent running on Windows: connects to the serial port and exposes enumeration, real-time streaming, and write capabilities over HTTP.
// This subcommand is meant to run on Windows (where the serial port physically exists); the serialcat main process pulls over plain HTTP as a cross-platform DAG engine.
package agent

import "time"

// Frame is a single serial record delivered over HTTP /api/stream (NDJSON).
type Frame struct {
	Ts   time.Time `json:"ts"`
	Text string    `json:"text"`
}
