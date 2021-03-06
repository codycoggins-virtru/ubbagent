// Copyright 2017 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package pipeline describes a metrics reporting pipeline that accepts reports as input and
// eventually delivers them (possibly after aggregation) to one or more downstream services.
// A pipeline generally consists of a collection of aggregators, dispatchers, and endpoints wrapped
// in RetryingSender objects. Metric values can be provided by an external source (reported via an
// API), or can be generated by a component such as a heartbeat.
//
//                  -> Aggregator -> ...        -> RetryingSender -> Endpoint A
// HTTP -> Selector -> Aggregator -> Dispatcher -> RetryingSender -> Endpoint B
//                  -> Aggregator -> ...        -> RetryingSender -> Endpoint C
//
//                     Heartbeat  -> Dispatcher -> RetryingSender -> Endpoint A
//
package pipeline

import (
	"sync"

	"github.com/GoogleCloudPlatform/ubbagent/metrics"
	"github.com/hashicorp/go-multierror"
)

// Input represents a Component that accepts reports from an external source.
type Input interface {
	// Input is also a Component.
	Component

	// AddReport adds a report to the pipeline. It returns an error if one is known immediately,
	// such as a report that refers to unknown metrics. See aggregator.Aggregator.
	AddReport(metrics.MetricReport) error
}

// Component represents a single component in a pipeline. Components can be used downstream of
// multiple other components, enabling creation of fork/join pipeline patterns. Because of this,
// components implement a reference counting strategy that determines when they should clean up
// underlying resources.
type Component interface {
	// Use registers new usage of this component. Use should be called whenever this component is
	// added downstream of some other component. When no longer used, Release should be called.
	Use()

	// Release is called when the caller is no longer using this component. If the component's usage
	// count reaches 0 due to this release, it should perform the following steps in order:
	// 1. Decrement the usage counter. If the usage counter is still greater than 0, return nil.
	// 2. Gracefully shutdown background processes and wait for completion. Following this step,
	//    no data shall be sent from this component to downstream components.
	// 3. Call Release on all downstream components, waiting for their release operations to
	//    complete.
	//
	// As a result, calling Release on all of the pipeline Input components should result in a graceful
	// shutdown of all Components in the correct order.
	//
	// Release returns an error if it or any of its downstream components generate one.
	Release() error
}

// Source represents an autonomous metric data source that runs within the Agent.
type Source interface {

	// Shutdown instructs the source to stop sending metric data, release any held components, and
	// clean up resources.
	Shutdown() error
}

// Type UsageTracker is a utility that helps track the usage of a Component. It provides Use and
// Release methods, and calls a close function when Release decrements the usage count to 0.
type UsageTracker struct {
	count int
	mu    sync.Mutex
}

func (u *UsageTracker) Use() {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.count < 0 {
		panic("UsageTracker is already closed")
	}
	u.count++
}

func (u *UsageTracker) Release(close func() error) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	// Check already closed condition
	if u.count < 0 {
		return nil
	}

	// Decrement usage. If the tracker was never Used, its count will now be -1. We still want to
	// call the Close function.
	u.count--
	if u.count <= 0 {
		u.count = -1
		return close()
	}

	return nil
}

// ReleaseAll calls Release on all of the given Components in parallel, returning a multierror if
// one or more calls fail, or nil if all succeed.
func ReleaseAll(components []Component) error {
	errors := make([]error, len(components))
	wg := sync.WaitGroup{}
	wg.Add(len(components))
	var i int
	for _, a := range components {
		go func(i int, a Component) {
			errors[i] = a.Release()
			wg.Done()
		}(i, a)
		i++
	}
	wg.Wait()
	return multierror.Append(nil, errors...).ErrorOrNil()
}
