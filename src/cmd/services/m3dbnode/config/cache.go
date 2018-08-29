// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package config

import "github.com/m3db/m3/src/dbnode/storage/series"

// CacheConfigurations is the cache configurations.
type CacheConfigurations struct {
	// Series cache policy.
	Series *SeriesCacheConfiguration `yaml:"series"`
}

// SeriesConfiguration returns the series cache configuration or default
// if none is specified.
func (c CacheConfigurations) SeriesConfiguration() SeriesCacheConfiguration {
	if c.Series == nil {
		// Return default cache configuration
		return SeriesCacheConfiguration{Policy: series.DefaultCachePolicy}
	}
	return *c.Series
}

// SeriesCacheConfiguration is the series cache configuration.
type SeriesCacheConfiguration struct {
	Policy series.CachePolicy                 `yaml:"policy"`
	LRU    *LRUSeriesCachePolicyConfiguration `yaml:"lru"`
}

// LRUSeriesCachePolicyConfiguration contains configuration for the LRU
// series caching policy.
type LRUSeriesCachePolicyConfiguration struct {
	MaxBlocks         uint `yaml:"maxBlocks" validate:"nonzero"`
	EventsChannelSize uint `yaml:"eventsChannelSize" validate:"nonzero"`
}
