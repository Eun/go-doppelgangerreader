package doppelgangerreader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
)

// DoppelgangerFactory is a reader that mimics the behaviour of an other reader
// it can be used to read readers multiple times
type DoppelgangerFactory interface {
	NewDoppelganger() io.ReadCloser
	RemoveDoppelganger(r io.ReadCloser) error
	Close() error
}

// NewFactory creates a new DoppelgangerFactory with the original reader specified
// if the reader is already a Doppelganger it will return the original factory
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

// NewDoppelganger creates a new reader that acts like the original reader
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

// RemoveDoppelganger a created reader from receiving new data
func (factory *doppelgangerFactory) RemoveDoppelganger(r io.ReadCloser) error {
	instance, ok := r.(*readerInstance)
	if !ok {
		return errors.New("not a reader instance")
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

	return errors.New("reader not found")
}

// Close the DoppelgangerFactory and stops all created Doppelgangers from receiving data
// (does not close the underlying reader)
func (factory *doppelgangerFactory) Close() error {
	// this is a public function so make sure we lock
	factory.mu.Lock()
	err := factory.close()
	factory.mu.Unlock()
	return err
}

// close the DoppelgangerFactory and stops all created Doppelgangers from receiving data
// (does not close the underlying reader)
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

// NilReaderError will be reported if the provided reader is nil
type NilReaderError struct{}

// Error returns the error message
func (NilReaderError) Error() string {
	return "Reader to mimic is nil"
}

// IsNilReaderError returns true if the specified error is a NilReaderError
func IsNilReaderError(e error) bool {
	_, ok := e.(NilReaderError)
	return ok
}

// GetFactory returns the DoppelgangerFactory if the reader is a Doppelganger
func GetFactory(reader io.Reader) DoppelgangerFactory {
	v, ok := reader.(*readerInstance)
	if !ok || v.DoppelBase == nil {
		return nil
	}

	// A detached reader is no longer attached to anything useful, so report no
	// factory. This preserves the behaviour from when detaching cleared
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
	for i := len(factory.readers) - 1; i >= 0; i-- {
		if err := factory.RemoveDoppelganger(factory.readers[i]); err != nil {
			return err
		}
	}
	factory.readers = nil
	return nil
}

// HTTPMiddleware adds a doppelganger factory for the body to the request.
// At the same time it replaces the original body with a doppelganger reader.
// You can specify a size limit for the reader (0 disables the limit)
// The factory can be fetched by using HTTPBodyFactory()
func HTTPMiddleware(handler http.Handler, limit int64) http.Handler {
	if handler == nil {
		panic("handler cannot be nil")
	}
	return httpMiddleware{handler, limit}
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
		body = io.LimitReader(r.Body, h.limit)
	}
	factory := NewFactory(body)
	r.Body = factory.NewDoppelganger()
	h.nextHandler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), httpMiddlewareFactoryContextKey, factory)))
	r.Body.Close()
	factory.Close()
}
