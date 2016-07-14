// Copyright (c) 2016 Uber Technologies, Inc.
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

package tsz

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/m3db/m3db/interfaces/m3db"
	xtime "github.com/m3db/m3db/x/time"
)

// readerIterator provides an interface for clients to incrementally
// read datapoints off of an encoded stream.
type readerIterator struct {
	is   *istream
	opts Options
	tess TimeEncodingSchemes
	mes  MarkerEncodingScheme

	// internal bookkeeping
	t    time.Time     // current time
	dt   time.Duration // current time delta
	vb   uint64        // current value
	xor  uint64        // current xor
	done bool          // has reached the end
	err  error         // current error

	ant       m3db.Annotation // current annotation
	tu        xtime.Unit      // current time unit
	tuChanged bool            // whether we have a new time unit

	closed bool
}

// NewReaderIterator returns a new iterator for a given reader
func NewReaderIterator(reader io.Reader, opts Options) m3db.ReaderIterator {
	return &readerIterator{
		is:   newIStream(reader),
		opts: opts,
		tess: opts.GetTimeEncodingSchemes(),
		mes:  opts.GetMarkerEncodingScheme(),
	}
}

// Next moves to the next item
func (it *readerIterator) Next() bool {
	if !it.hasNext() {
		return false
	}
	it.ant = nil
	it.tuChanged = false
	if it.t.IsZero() {
		it.readFirstTimestamp()
		it.readFirstValue()
	} else {
		it.readNextTimestamp()
		it.readNextValue()
	}
	// NB(xichen): reset time delta to 0 when there is a time unit change to be
	// consistent with the encoder.
	if it.tuChanged {
		it.dt = 0
	}
	return it.hasNext()
}

func (it *readerIterator) readFirstTimestamp() {
	nt := int64(it.readBits(64))
	// NB(xichen): first time stamp is always normalized to nanoseconds.
	st := xtime.FromNormalizedTime(nt, time.Nanosecond)
	it.tu = initialTimeUnit(st, it.opts.GetDefaultTimeUnit())
	it.readNextTimestamp()
	it.t = st.Add(it.dt)
}

func (it *readerIterator) readFirstValue() {
	it.vb = it.readBits(64)
	it.xor = it.vb
}

func (it *readerIterator) readNextTimestamp() {
	it.dt += it.readMarkerOrDeltaOfDelta()
	it.t = it.t.Add(it.dt)
}

func (it *readerIterator) tryReadMarker() (time.Duration, bool) {
	numBits := it.mes.NumOpcodeBits() + it.mes.NumValueBits()
	opcodeAndValue, success := it.tryPeekBits(numBits)
	if !success {
		return 0, false
	}
	opcode := opcodeAndValue >> uint(it.mes.NumValueBits())
	if opcode != it.mes.Opcode() {
		return 0, false
	}
	valueMask := (1 << uint(it.mes.NumValueBits())) - 1
	markerValue := int64(opcodeAndValue & uint64(valueMask))
	switch Marker(markerValue) {
	case it.mes.EndOfStream():
		it.readBits(numBits)
		it.done = true
		return 0, true
	case it.mes.Annotation():
		it.readBits(numBits)
		it.readAnnotation()
		return it.readMarkerOrDeltaOfDelta(), true
	case it.mes.TimeUnit():
		it.readBits(numBits)
		it.readTimeUnit()
		return it.readMarkerOrDeltaOfDelta(), true
	default:
		return 0, false
	}
}

func (it *readerIterator) readMarkerOrDeltaOfDelta() time.Duration {
	if dod, success := it.tryReadMarker(); success {
		return dod
	}
	tes, exists := it.tess[it.tu]
	if !exists {
		it.err = fmt.Errorf("time encoding scheme for time unit %v doesn't exist", it.tu)
		return 0
	}
	return it.readDeltaOfDelta(tes)
}

func (it *readerIterator) readDeltaOfDelta(tes TimeEncodingScheme) (d time.Duration) {
	if it.tuChanged {
		// NB(xichen): if the time unit has changed, always read 64 bits as normalized
		// dod in nanoseconds.
		dod := signExtend(it.readBits(64), 64)
		return time.Duration(dod)
	}

	cb := it.readBits(1)
	if cb == tes.ZeroBucket().Opcode() {
		return 0
	}
	buckets := tes.Buckets()
	for i := 0; i < len(buckets); i++ {
		cb = (cb << 1) | it.readBits(1)
		if cb == buckets[i].Opcode() {
			dod := signExtend(it.readBits(buckets[i].NumValueBits()), buckets[i].NumValueBits())
			return xtime.FromNormalizedDuration(dod, it.timeUnit())
		}
	}
	numValueBits := tes.DefaultBucket().NumValueBits()
	dod := signExtend(it.readBits(numValueBits), numValueBits)
	return xtime.FromNormalizedDuration(dod, it.timeUnit())
}

func (it *readerIterator) readNextValue() {
	it.xor = it.readXOR()
	it.vb ^= it.xor
}

func (it *readerIterator) readAnnotation() {
	// NB: we add 1 here to offset the 1 we subtracted during encoding
	antLen := it.readVarint() + 1
	if it.hasError() {
		return
	}
	if antLen <= 0 {
		it.err = fmt.Errorf("unexpected annotation length %d", antLen)
		return
	}
	// TODO(xichen): use pool to allocate the buffer once the pool diff lands.
	buf := make([]byte, antLen)
	for i := 0; i < antLen; i++ {
		buf[i] = byte(it.readBits(8))
	}
	it.ant = buf
}

func (it *readerIterator) readTimeUnit() {
	tu := xtime.Unit(it.readBits(8))
	if tu.IsValid() && tu != it.tu {
		it.tuChanged = true
	}
	it.tu = tu
}

func (it *readerIterator) readXOR() uint64 {
	cb := it.readBits(1)
	if cb == opcodeZeroValueXOR {
		return 0
	}

	cb = (cb << 1) | it.readBits(1)
	if cb == opcodeContainedValueXOR {
		previousLeading, previousTrailing := leadingAndTrailingZeros(it.xor)
		numMeaningfulBits := 64 - previousLeading - previousTrailing
		return it.readBits(numMeaningfulBits) << uint(previousTrailing)
	}

	numLeadingZeros := int(it.readBits(6))
	numMeaningfulBits := int(it.readBits(6)) + 1
	numTrailingZeros := 64 - numLeadingZeros - numMeaningfulBits
	meaningfulBits := it.readBits(numMeaningfulBits)
	return meaningfulBits << uint(numTrailingZeros)
}

func (it *readerIterator) readBits(numBits int) uint64 {
	if !it.hasNext() {
		return 0
	}
	var res uint64
	res, it.err = it.is.ReadBits(numBits)
	return res
}

func (it *readerIterator) readVarint() int {
	if !it.hasNext() {
		return 0
	}
	var res int64
	res, it.err = binary.ReadVarint(it.is)
	return int(res)
}

func (it *readerIterator) tryPeekBits(numBits int) (uint64, bool) {
	if !it.hasNext() {
		return 0, false
	}
	res, err := it.is.PeekBits(numBits)
	if err != nil {
		return 0, false
	}
	return res, true
}

func (it *readerIterator) timeUnit() time.Duration {
	if it.hasError() {
		return 0
	}
	var tu time.Duration
	tu, it.err = it.tu.Value()
	return tu
}

// Current returns the value as well as the annotation associated with the current datapoint.
// Users should not hold on to the returned Annotation object as it may get invalidated when
// the iterator calls Next().
func (it *readerIterator) Current() (m3db.Datapoint, xtime.Unit, m3db.Annotation) {
	return m3db.Datapoint{
		Timestamp: it.t,
		Value:     math.Float64frombits(it.vb),
	}, it.tu, it.ant
}

// Err returns the error encountered
func (it *readerIterator) Err() error {
	return it.err
}

func (it *readerIterator) hasError() bool {
	return it.err != nil
}

func (it *readerIterator) isDone() bool {
	return it.done
}

func (it *readerIterator) isClosed() bool {
	return it.closed
}

func (it *readerIterator) hasNext() bool {
	return !it.hasError() && !it.isDone() && !it.isClosed()
}

func (it *readerIterator) Reset(reader io.Reader) {
	it.is.Reset(reader)
	it.t = time.Time{}
	it.dt = 0
	it.vb = 0
	it.xor = 0
	it.done = false
	it.err = nil
	it.ant = nil
	it.tu = xtime.None
	it.closed = false
}

func (it *readerIterator) Close() {
	if it.closed {
		return
	}
	it.closed = true
	pool := it.opts.GetReaderIteratorPool()
	if pool != nil {
		pool.Put(it)
	}
}