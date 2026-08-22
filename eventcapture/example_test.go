package eventcapture_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/eventcapture"
)

// servedRequest is the caller's event type. eventcapture prescribes nothing
// about an event's shape or meaning.
type servedRequest struct {
	At     time.Time
	Route  string
	Status int
}

// sliceSink is a Sink that keeps records in memory. Deployments use
// eventcapture/jsonl or an exporter instead; the contract is these same three
// methods, all called from the Recorder's single flusher goroutine.
type sliceSink struct {
	lines []string
}

func (s *sliceSink) Write(record any) error {
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	s.lines = append(s.lines, string(line))

	return nil
}

func (s *sliceSink) Flush() error { return nil }

func (s *sliceSink) Close() error { return nil }

// wireRequest is the projection actually written to the sink: a wire-shaped
// struct with stable JSON tags, built off the hot path.
type wireRequest struct {
	Route  string `json:"route"`
	Status int    `json:"status"`
}

// Example captures two events and drains them. Record is a non-blocking
// channel send, so the request path pays nothing but the send; the flusher
// goroutine started by Run does the projecting and writing.
func Example() {
	sink := &sliceSink{}

	rec, err := eventcapture.NewRecorder[servedRequest](sink,
		eventcapture.WithTransform(func(ev *servedRequest) any {
			return wireRequest{Route: ev.Route, Status: ev.Status}
		}),
	)
	if err != nil {
		panic(err)
	}

	go rec.Run()

	rec.Record(&servedRequest{Route: "/widgets", Status: 200})
	rec.Record(&servedRequest{Route: "/widgets/1", Status: 404})

	// Close belongs after the server has finished draining in-flight requests,
	// not tied to a server context: it empties the buffer, runs a final flush,
	// and closes the sink.
	if err = rec.Close(context.Background()); err != nil {
		panic(err)
	}

	for _, line := range sink.lines {
		fmt.Println(line)
	}
	fmt.Println("dropped:", rec.Dropped())
	// Output:
	// {"route":"/widgets","status":200}
	// {"route":"/widgets/1","status":404}
	// dropped: 0
}

// counts is the caller's counter type, folded per (route, minute) cell.
type counts struct {
	Requests int `json:"requests"`
	Errors   int `json:"errors"`
}

// rollup is the record emitted for one completed aggregation bucket.
type rollup struct {
	Minute string `json:"minute"`
	Route  string `json:"route"`
	Counts counts `json:"counts"`
}

// ExampleNewRecorder_aggregation composes an Aggregator into the flusher.
// WithObserver folds every event into its cell and WithOnFlush emits completed
// buckets — both run in the flusher goroutine, which is what makes the
// lock-free Aggregator safe. WithoutRawRecords keeps the individual events out
// of the sink, so only the rollups are written.
func ExampleNewRecorder_aggregation() {
	sink := &sliceSink{}
	start := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)

	agg := eventcapture.NewAggregator[string, counts](time.Minute, 10_000,
		eventcapture.WithKeyOrder(strings.Compare),
	)

	rec, err := eventcapture.NewRecorder[servedRequest](sink,
		// A flush interval longer than the example keeps the periodic tick from
		// splitting the rollups across two flushes; a real deployment leaves the
		// default cadence alone.
		eventcapture.WithFlushInterval(time.Hour),
		eventcapture.WithoutRawRecords(),
		// Without this the Aggregator silently discards observations once
		// maxKeys is reached; the Recorder cannot see inside the composition
		// on its own.
		eventcapture.WithOverflowSource(agg.TakeOverflow),
		eventcapture.WithObserver(func(ev *servedRequest) {
			agg.Observe(ev.Route, ev.At, func(c *counts) {
				c.Requests++
				if ev.Status >= 400 {
					c.Errors++
				}
			})
		}),
		eventcapture.WithOnFlush(func(now time.Time, final bool, emit func(record any)) {
			for _, b := range agg.Flush(now, final) {
				emit(rollup{
					Minute: b.Start.Format(time.RFC3339),
					Route:  b.Key,
					Counts: b.Counts,
				})
			}
		}),
	)
	if err != nil {
		panic(err)
	}

	go rec.Run()

	rec.Record(&servedRequest{At: start.Add(10 * time.Second), Route: "/widgets", Status: 200})
	rec.Record(&servedRequest{At: start.Add(20 * time.Second), Route: "/widgets", Status: 500})
	rec.Record(&servedRequest{At: start.Add(30 * time.Second), Route: "/gadgets", Status: 200})

	// The final flush passes all=true, so the still-open minute is emitted
	// rather than lost at shutdown.
	if err = rec.Close(context.Background()); err != nil {
		panic(err)
	}

	for _, line := range sink.lines {
		fmt.Println(line)
	}
	// Output:
	// {"minute":"2026-01-01T12:00:00Z","route":"/gadgets","counts":{"requests":1,"errors":0}}
	// {"minute":"2026-01-01T12:00:00Z","route":"/widgets","counts":{"requests":2,"errors":1}}
}
