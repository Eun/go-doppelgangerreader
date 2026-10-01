package doppelgangerreader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
)

// DoppelgangerFactory is a reader that mimics the behavior of an other reader
// it can be used to read readers multiple times.
type DoppelgangerFactory interface {
	NewDoppelganger() io.ReadCloser
	RemoveDoppelganger(r io.ReadCloser) error
	Close() error
}

// NewFactory creates a new DoppelgangerFactory with the original reader specified
// if the reader is already a Doppelganger it will return the original factory.
func NewFactory(readerToMimic io.Reader) DoppelgangerFactory {
	factory := GetFactory(readerToMimic)
	if factory != nil {
		return &nestedDoppelgangerFactory{
			parent: factory,
		}
	}
	return &doppelgangerFactory{
		source: readerToMimic,
	}
}

type doppelgangerFactory struct {
	source   io.Reader
	readers  []*readerInstance
	buffer   bytes.Buffer
	mu       sync.Mutex
	closedOn *int
}

// NewDoppelganger creates a new reader that acts like the original reader.
func (factory *doppelgangerFactory) NewDoppelganger() io.ReadCloser {
	factory.mu.Lock()
	reader := &readerInstance{
		DoppelBase: factory,
		// prefill Buffer with already collected data
		Buffer: bytes.NewBuffer(factory.buffer.Bytes()),
	}
	if factory.closedOn == nil {
		// only add to readers if there is still data to consume
		factory.readers = append(factory.readers, reader)
	}
	factory.mu.Unlock()
	return reader
}

// RemoveDoppelganger a created reader from receiving new data.
func (factory *doppelgangerFactory) RemoveDoppelganger(r io.ReadCloser) error {
	instance, ok := r.(*readerInstance)
	if !ok {
		return NotAReaderInstanceError{}
	}
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.removeLocked(instance)
}

// removeLocked detaches a reader. The caller must hold factory.mu.
func (factory *doppelgangerFactory) removeLocked(instance *readerInstance) error {
	for i := len(factory.readers) - 1; i >= 0; i-- {
		if factory.readers[i] == instance {
			factory.readers[i].detached = true
			factory.readers = append(factory.readers[:i], factory.readers[i+1:]...)
			return nil
		}
	}

	return ReaderNotFoundError{}
}

// Close the DoppelgangerFactory and stops all created Doppelgangers from receiving data
// (does not close the underlying reader).
func (factory *doppelgangerFactory) Close() error {
	// this is a public function so make sure we lock
	factory.mu.Lock()
	err := factory.close()
	factory.mu.Unlock()
	return err
}

// close the DoppelgangerFactory and stops all created Doppelgangers from receiving data
// (does not close the underlying reader).
func (factory *doppelgangerFactory) close() error {
	// we already closed
	if factory.closedOn != nil {
		return nil
	}
	factory.closedOn = new(int)
	*factory.closedOn = factory.buffer.Len()

	// remove all readers because everything has been consumed
	factory.readers = nil
	return nil
}

func (factory *doppelgangerFactory) read(caller *readerInstance, p []byte) (int, error) {
	if factory.closedOn != nil {
		return 0, io.EOF
	}
	if factory.source == nil {
		return 0, NilReaderError{}
	}
	n, err := factory.source.Read(p)

	if n > 0 {
		// fill my own Buffer if we have data
		factory.buffer.Write(p[:n])
	}

	if err != nil {
		return n, err
	}

	for i := len(factory.readers) - 1; i >= 0; i-- {
		if factory.readers[i] != caller {
			factory.readers[i].Buffer.Write(p[:n])
		}
	}
	return n, nil
}

type readerInstance struct {
	DoppelBase *doppelgangerFactory
	Buffer     *bytes.Buffer
	// detached is set once the reader has been removed from its factory, in
	// place of clearing DoppelBase. DoppelBase has to stay readable without
	// the lock (Read needs it to find the mutex in the first place), so
	// mutating it was an unavoidable data race. detached is only ever touched
	// while holding DoppelBase.mu.
	detached bool
}

func (r *readerInstance) Read(p []byte) (n int, err error) {
	if r.DoppelBase == nil {
		return 0, io.EOF
	}
	r.DoppelBase.mu.Lock()
	defer r.DoppelBase.mu.Unlock()

	// Checked under the lock: RemoveDoppelganger can detach this reader
	// concurrently.
	if r.detached {
		return 0, io.EOF
	}

	if r.Buffer.Len() > 0 {
		n, err = r.Buffer.Read(p)
	} else {
		n, err = r.DoppelBase.read(r, p)
		if err != nil {
			r.DoppelBase.close()
		}
	}
	return n, err
}

func (r *readerInstance) Close() error {
	// A nil factory means the reader was never attached, so there is nothing
	// to detach from.
	if r.DoppelBase == nil {
		return nil
	}

	r.DoppelBase.mu.Lock()
	defer r.DoppelBase.mu.Unlock()

	// Already detached by a previous Close or by RemoveDoppelganger: closing
	// again is a no-op, matching the usual io.Closer expectation. This is the
	// guard added in #1, now moved under the lock and keyed on the detached
	// flag rather than a cleared DoppelBase.
	if r.detached {
		return nil
	}

	// if the factory is already closed
	// we dont need to remove
	if r.DoppelBase.closedOn == nil {
		// Inlined rather than calling RemoveDoppelganger, which would take the
		// same non-reentrant mutex.
		return r.DoppelBase.removeLocked(r)
	}
	return nil
}

// ReaderNotFoundError will be reported if the reader is not (or no longer)
// registered with the factory. A reader that has been closed, removed, or
// whose factory has already closed is no longer registered, so this is not
// necessarily a caller error.
type ReaderNotFoundError struct{}

// Error returns the error message.
func (ReaderNotFoundError) Error() string {
	return "reader not found"
}

// Is reports whether target is a ReaderNotFoundError, so errors.Is works on
// wrapped errors.
func (ReaderNotFoundError) Is(target error) bool {
	_, ok := target.(ReaderNotFoundError)
	return ok
}

// IsReaderNotFoundError returns true if the specified error is a
// ReaderNotFoundError.
func IsReaderNotFoundError(e error) bool {
	var t ReaderNotFoundError
	return errors.As(e, &t)
}

// NotAReaderInstanceError will be reported if the reader was not created by a
// DoppelgangerFactory.
type NotAReaderInstanceError struct{}

// Error returns the error message.
func (NotAReaderInstanceError) Error() string {
	return "not a reader instance"
}

// Is reports whether target is a NotAReaderInstanceError, so errors.Is works
// on wrapped errors.
func (NotAReaderInstanceError) Is(target error) bool {
	_, ok := target.(NotAReaderInstanceError)
	return ok
}

// IsNotAReaderInstanceError returns true if the specified error is a
// NotAReaderInstanceError.
func IsNotAReaderInstanceError(e error) bool {
	var t NotAReaderInstanceError
	return errors.As(e, &t)
}

// NilReaderError will be reported if the provided reader is nil.
type NilReaderError struct{}

// Error returns the error message.
func (NilReaderError) Error() string {
	return "Reader to mimic is nil"
}

// Is reports whether target is a NilReaderError, so errors.Is works on
// wrapped errors.
func (NilReaderError) Is(target error) bool {
	_, ok := target.(NilReaderError)
	return ok
}

// IsNilReaderError returns true if the specified error is a NilReaderError.
func IsNilReaderError(e error) bool {
	var t NilReaderError
	return errors.As(e, &t)
}

// GetFactory returns the DoppelgangerFactory if the reader is a Doppelganger.
func GetFactory(reader io.Reader) DoppelgangerFactory {
	v, ok := reader.(*readerInstance)
	if !ok || v.DoppelBase == nil {
		return nil
	}

	// A detached reader is no longer attached to anything useful, so report no
	// factory. This preserves the behavior from when detaching cleared
	// DoppelBase outright; the field is now left intact because Read needs it
	// to reach the mutex, and mutating it was a data race.
	v.DoppelBase.mu.Lock()
	defer v.DoppelBase.mu.Unlock()
	if v.detached {
		return nil
	}
	return v.DoppelBase
}

type nestedDoppelgangerFactory struct {
	parent  DoppelgangerFactory
	readers []io.ReadCloser
}

func (factory *nestedDoppelgangerFactory) NewDoppelganger() io.ReadCloser {
	r := factory.parent.NewDoppelganger()
	factory.readers = append(factory.readers, r)
	return r
}

func (factory *nestedDoppelgangerFactory) RemoveDoppelganger(r io.ReadCloser) error {
	return factory.parent.RemoveDoppelganger(r)
}

func (factory *nestedDoppelgangerFactory) Close() error {
	// Every reader is removed even if one of them reports an error, so a
	// single failure cannot leak the rest. The first real error is returned
	// once the loop has finished.
	//
	// ErrReaderNotFound is expected rather than exceptional here: the parent
	// drops its readers when it closes, which happens as soon as the source
	// reaches EOF, and a caller may also have closed a reader itself. Treating
	// that as a failure made Close report an error after a perfectly ordinary
	// read-to-completion.
	var firstErr error
	for i := len(factory.readers) - 1; i >= 0; i-- {
		if err := factory.RemoveDoppelganger(factory.readers[i]); err != nil {
			if IsReaderNotFoundError(err) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	factory.readers = nil
	return firstErr
}

// HTTPMiddleware adds a doppelganger factory for the body to the request.
// At the same time it replaces the original body with a doppelganger reader.
// You can specify a size limit for the reader (0 disables the limit)
// The factory can be fetched by using HTTPBodyFactory()
//
// A body that exceeds the limit is reported as BodyTooLargeError on the next
// Read rather than being silently truncated, so a handler cannot mistake a
// truncated body for a complete one. Use IsBodyTooLargeError to detect it.
func HTTPMiddleware(handler http.Handler, limit int64) http.Handler {
	if handler == nil {
		panic("handler cannot be nil")
	}
	return httpMiddleware{handler, limit}
}

// BodyTooLargeError is reported when the request body exceeds the limit given
// to HTTPMiddleware.
type BodyTooLargeError struct {
	// Limit is the configured maximum, in bytes.
	Limit int64
}

// Error returns the error message.
func (e BodyTooLargeError) Error() string {
	return "http: request body too large"
}

// Is reports whether target is a BodyTooLargeError, so errors.Is works on
// wrapped errors.
//
// Limit is deliberately not compared: without this method errors.Is falls back
// to struct equality, so the natural check
// errors.Is(err, BodyTooLargeError{}) would be false for any non-zero limit.
// Use errors.As when the limit itself is needed.
func (BodyTooLargeError) Is(target error) bool {
	_, ok := target.(BodyTooLargeError)
	return ok
}

// IsBodyTooLargeError returns true if the specified error is a
// BodyTooLargeError.
func IsBodyTooLargeError(e error) bool {
	var t BodyTooLargeError
	return errors.As(e, &t)
}

// limitedReader reads at most Limit bytes and then reports
// BodyTooLargeError, instead of the silent io.EOF that io.LimitReader gives.
//
// The distinction matters whenever the body is authenticated: truncating it
// silently means a handler verifies a signature over different bytes than the
// client sent, and cannot tell.
type limitedReader struct {
	R         io.Reader
	Limit     int64
	remaining int64
	exceeded  bool
}

func newLimitedReader(r io.Reader, limit int64) *limitedReader {
	return &limitedReader{R: r, Limit: limit, remaining: limit}
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.exceeded {
		return 0, BodyTooLargeError{Limit: l.Limit}
	}

	// Read one byte beyond the limit so going over can be detected, rather
	// than stopping exactly at it and looking like a clean end of stream.
	if int64(len(p)) > l.remaining+1 {
		p = p[:l.remaining+1]
	}

	n, err := l.R.Read(p)
	if n > 0 {
		if int64(n) > l.remaining {
			// Discard the overflow byte; the body is over the limit.
			l.exceeded = true
			l.remaining = 0
			return 0, BodyTooLargeError{Limit: l.Limit}
		}
		l.remaining -= int64(n)
	}
	return n, err
}

// HTTPBodyFactory returns a http body factory for a request.
// Notice that you have to use the HTTPMiddleware function.
func HTTPBodyFactory(r *http.Request) DoppelgangerFactory {
	v := r.Context().Value(httpMiddlewareFactoryContextKey)
	if v == nil {
		return nil
	}
	return v.(DoppelgangerFactory)
}

type httpMiddlewareFactoryContextType struct{}

var httpMiddlewareFactoryContextKey httpMiddlewareFactoryContextType = struct{}{}

type httpMiddleware struct {
	nextHandler http.Handler
	limit       int64
}

func (h httpMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		h.nextHandler.ServeHTTP(w, r)
		return
	}

	var body io.Reader = r.Body
	if h.limit > 0 {
		body = newLimitedReader(r.Body, h.limit)
	}
	factory := NewFactory(body)
	r.Body = factory.NewDoppelganger()
	h.nextHandler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), httpMiddlewareFactoryContextKey, factory)))
	r.Body.Close()
	factory.Close()
}
