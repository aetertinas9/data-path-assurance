// Package app holds the offline GPU fleet explain use case.
//
// ExplainFleet takes the ratified domain values of one offline replay (the
// snapshot frames of a single node, the collector trust profile, the fleet
// policy and the device intents) from a ReplaySource, replays them through the
// ratified fleet API (snapshot admission, cumulative evidence window,
// per-frame topology, link-width findings, device evaluation and node
// aggregation), and assembles the explanation model that the CLI adapters
// render as JSON or text.
//
// The package knows nothing about files, JSON or terminals: its inputs are
// domain values and its output is a plain model. Explanation-only judgements
// made here (trusted hop selection, limitations, sorting and truncation) never
// feed back into the decisions computed by the fleet API.
package app
