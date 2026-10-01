package doppelgangerreader_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Eun/go-doppelgangerreader"
)

func readAtLeast(t *testing.T, r io.Reader, size int) []byte {
	buf := make([]byte, size)
	n, err := io.ReadAtLeast(r, buf, size)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}

func read(t *testing.T, r io.Reader, size int) []byte {
	buf := make([]byte, size)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}

func TestDoppelganger(t *testing.T) {
	reader := doppelgangerreader.NewFactory(rand.Reader)
	defer reader.Close()

	reader1 := reader.NewDoppelganger()
	// read 10
	buf1 := readAtLeast(t, reader1, 10)

	// create a new reader and read 20
	reader2 := reader.NewDoppelganger()
	buf2 := readAtLeast(t, reader2, 20)

	// the first 10 bytes should be equal
	if !bytes.Equal(buf1, buf2[:10]) {
		t.Fatalf("expected %v, but got %v", buf1, buf2[:10])
	}

	// read more on reader1
	buf1 = readAtLeast(t, reader1, 10)
	if !bytes.Equal(buf1, buf2[10:]) {
		t.Fatalf("expected %v, but got %v", buf1, buf2[10:])
	}

	// read more on reader2
	buf2 = readAtLeast(t, reader2, 10)
	buf1 = readAtLeast(t, reader1, 10)
	if !bytes.Equal(buf2, buf1) {
		t.Fatalf("expected %v, but got %v", buf2, buf1)
	}
}

func TestReaderInstanceClose(t *testing.T) {
	reader := doppelgangerreader.NewFactory(rand.Reader)
	defer reader.Close()

	reader1 := reader.NewDoppelganger()
	reader2 := reader.NewDoppelganger()
	buf1 := readAtLeast(t, reader1, 10)
	reader1.Close()

	buf2 := readAtLeast(t, reader2, 10)

	if !bytes.Equal(buf1, buf2) {
		t.Fatalf("expected %v, but got %v", buf1, buf2)
	}

	_, err := reader1.Read(buf1)
	if err != io.EOF {
		t.Fatalf("expected io.EOF, but got %T", err)
	}
}

type failReaderAfterN struct {
	N int
	p int
	io.Reader
}

func (r *failReaderAfterN) Read(p []byte) (int, error) {
	if r.p >= r.N {
		return 0, io.EOF
	}
	n, err := r.Reader.Read(p)
	if err != nil {
		return n, err
	}

	if n > 0 {
		r.p += n
	}
	return n, err
}

func TestCloseAfterFail(t *testing.T) {
	r := &failReaderAfterN{
		N:      10,
		Reader: rand.Reader,
	}

	reader := doppelgangerreader.NewFactory(r)
	defer reader.Close()

	reader1 := reader.NewDoppelganger()
	buf1 := read(t, reader1, 10)
	n, err := reader1.Read(buf1)
	if err != io.EOF {
		t.Fatalf("expected io.EOF, but got %T", err)
	}
	if n != 0 {
		t.Fatalf("expected 0, but got %d", n)
	}

	// do it again with another reader, we should expect the same behaviour
	reader2 := reader.NewDoppelganger()
	buf2 := read(t, reader2, 10)
	n, err = reader2.Read(buf2)
	if err != io.EOF {
		t.Fatalf("expected io.EOF, but got %T", err)
	}
	if n != 0 {
		t.Fatalf("expected 0, but got %d", n)
	}

	if !bytes.Equal(buf1, buf2) {
		t.Fatalf("expected %v, but got %v", buf1, buf2)
	}
}

type dummyReader struct {
	closed bool
}

func (*dummyReader) Read([]byte) (int, error) {
	return 0, errors.New("not implemented")
}

func (r *dummyReader) Close() error {
	r.closed = true
	return nil
}

func TestCloseOnSource(t *testing.T) {
	dummy := &dummyReader{}
	factory := doppelgangerreader.NewFactory(dummy)
	if err := factory.Close(); err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}
	if dummy.closed {
		t.Fatalf("expected dummy not to be closed")
	}
}

func TestDoppelganger_RemoveReader(t *testing.T) {
	t.Run("invalid reader", func(t *testing.T) {
		reader := doppelgangerreader.NewFactory(rand.Reader)
		defer reader.Close()

		if err := reader.RemoveDoppelganger(ioutil.NopCloser(bytes.NewBuffer(nil))); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("already removed reader", func(t *testing.T) {
		reader := doppelgangerreader.NewFactory(rand.Reader)
		defer reader.Close()

		r1 := reader.NewDoppelganger()
		if err := reader.RemoveDoppelganger(r1); err != nil {
			t.Fatalf("expected no error, but got %v", nil)
		}

		if err := reader.RemoveDoppelganger(r1); err == nil {
			t.Fatalf("expected error")
		}
	})
}

func TestConcurrent(t *testing.T) {
	factory := doppelgangerreader.NewFactory(rand.Reader)

	type Result struct {
		Data  []byte
		Size  int
		Error error
	}

	var resultData sync.Map

	var wg sync.WaitGroup

	read := func(i int, size int) {
		buf := make([]byte, size)
		n, err := io.ReadAtLeast(factory.NewDoppelganger(), buf, size)

		// fmt.Printf("%d: %d %s (%d) %v\n", i, size, string(buf[:n]), n, err)

		resultData.Store(i, &Result{
			Error: err,
			Size:  size,
			Data:  buf[:n],
		})
		wg.Done()
	}

	// generates a random number between 10 and 20
	randSize := func() int {
		max := big.NewInt(10)
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			t.Fatal(err)
		}
		return int(n.Int64()) + 10
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go read(i, randSize())
	}

	wg.Wait()

	resultData.Range(func(key, value interface{}) bool {
		a := value.(*Result)

		if a.Error != nil {
			t.Fatalf("expected no error %v, but got %v", key, a.Error)
		}

		if a.Size != len(a.Data) {
			t.Fatalf("expected %d, but got %d", a.Size, len(a.Data))
		}

		// check if data is correct
		resultData.Range(func(k, value interface{}) bool {
			if !bytes.Equal(a.Data[:10], value.(*Result).Data[:10]) {
				t.Fatalf("expected %v (%v), but got %v (%v)", a.Data[:10], key, value.(*Result).Data[:10], k)
			}
			return true
		})

		return true
	})
}

func TestReadAfterSourceIsClosed(t *testing.T) {
	factory := doppelgangerreader.NewFactory(bytes.NewBufferString("Hello World"))

	// consume everything
	_, err := ioutil.ReadAll(factory.NewDoppelganger())
	if err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}

	if err := factory.Close(); err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}
	buf, err := ioutil.ReadAll(factory.NewDoppelganger())
	if err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}
	if !bytes.Equal([]byte("Hello World"), buf) {
		t.Fatalf("expected %v, but got %v", []byte("Hello World"), buf)
	}
}

func TestReaderCloseAfterFactoryClose(t *testing.T) {
	factory := doppelgangerreader.NewFactory(bytes.NewReader(nil))
	reader := factory.NewDoppelganger()
	if err := factory.Close(); err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}
}

type eofReader struct{}

func (eofReader) Read(p []byte) (int, error) {
	copy(p, []byte{1, 2, 3})
	return 3, io.EOF
}

func TestFillBufferEOFOnFirstCall(t *testing.T) {
	factory := doppelgangerreader.NewFactory(eofReader{})
	defer factory.Close()

	buf1, err := ioutil.ReadAll(factory.NewDoppelganger())
	if err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}

	buf2, err := ioutil.ReadAll(factory.NewDoppelganger())
	if err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}

	if !bytes.Equal(buf1, buf2) {
		t.Fatalf("expected %v, but got %v", buf1, buf2)
	}
}

func TestHttpMultipartReader(t *testing.T) {
	// parts from mime/multipart/writer_test.go (go1.12.5)
	fileContents := []byte("my file contents")

	m := http.NewServeMux()
	m.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		factory := doppelgangerreader.NewFactory(request.Body)
		defer factory.Close()

		request.Body = factory.NewDoppelganger()

		r, err := request.MultipartReader()
		if err != nil {
			t.Fatalf("expected no error, but got %v", err)
		}

		part, err := r.NextPart()
		if err != nil {
			t.Fatalf("part 1: %v", err)
		}
		if g, e := part.FormName(), "myfile"; g != e {
			t.Errorf("part 1: want form name %q, got %q", e, g)
		}
		slurp, err := ioutil.ReadAll(part)
		if err != nil {
			t.Fatalf("part 1: ReadAll: %v", err)
		}
		if e, g := string(fileContents), string(slurp); e != g {
			t.Errorf("part 1: want contents %q, got %q", e, g)
		}

		part, err = r.NextPart()
		if err != nil {
			t.Fatalf("part 2: %v", err)
		}
		if g, e := part.FormName(), "key"; g != e {
			t.Errorf("part 2: want form name %q, got %q", e, g)
		}
		slurp, err = ioutil.ReadAll(part)
		if err != nil {
			t.Fatalf("part 2: ReadAll: %v", err)
		}
		if e, g := "val", string(slurp); e != g {
			t.Errorf("part 2: want contents %q, got %q", e, g)
		}

		part, err = r.NextPart()
		if part != nil || err == nil {
			t.Fatalf("expected end of parts; got %v, %v", part, err)
		}
	})
	s := httptest.NewServer(m)
	defer s.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	{
		part, err := w.CreateFormFile("myfile", "my-file.txt")
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		part.Write(fileContents)
		err = w.WriteField("key", "val")
		if err != nil {
			t.Fatalf("WriteField: %v", err)
		}
		part.Write([]byte("val"))
		err = w.Close()
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		s := buf.String()
		if len(s) == 0 {
			t.Fatalf("String: unexpected empty result")
		}
		if s[0] == '\r' || s[0] == '\n' {
			t.Fatalf("String: unexpected newline")
		}
	}

	_, err := s.Client().Post(s.URL, w.FormDataContentType(), &buf)
	if err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}
}

func TestNilReader(t *testing.T) {
	factory := doppelgangerreader.NewFactory(nil)
	defer factory.Close()
	reader := factory.NewDoppelganger()
	var buf [8]byte
	n, err := reader.Read(buf[:])
	if n != 0 {
		t.Fatalf("expected 0, but got %d", n)
	}

	if !doppelgangerreader.IsNilReaderError(err) {
		t.Fatalf("expected error, but got %v", err)
	}
	if err.Error() != "Reader to mimic is nil" {
		t.Fatalf("expected `Reader to mimic is nil' error, got %v", err.Error())
	}
}

func TestConsumeSource(t *testing.T) {
	data := []byte("Hello World")
	source := bytes.NewBuffer(data)
	factory := doppelgangerreader.NewFactory(source)
	defer factory.Close()

	b, err := ioutil.ReadAll(factory.NewDoppelganger())
	if err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}
	if !bytes.Equal(data, b) {
		t.Fatalf("expected %v, but got %v", data, b)
	}

	b, err = ioutil.ReadAll(source)
	if err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}
	if !bytes.Equal([]byte{}, b) {
		t.Fatalf("expected %v, but got %v", []byte{}, b)
	}
}

type testErrorHandler struct {
	Error       interface{}
	Body        []byte
	NextHandler http.Handler
}

func (e *testErrorHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	factory := doppelgangerreader.NewFactory(request.Body)
	request.Body = factory.NewDoppelganger()
	defer func() {
		var err error
		e.Error = recover()
		e.Body, err = ioutil.ReadAll(factory.NewDoppelganger())
		if err != nil {
			panic(err)
		}
		factory.Close()
	}()
	e.NextHandler.ServeHTTP(writer, request)
}

func TestHttpHandlerRecover(t *testing.T) {
	payload := []byte("Hello World")
	panicError := "some error"
	handler := http.NewServeMux()
	handler.HandleFunc("/", func(w http.ResponseWriter, request *http.Request) {
		_, _ = ioutil.ReadAll(request.Body)
		panic(panicError)
	})

	errorHandler := &testErrorHandler{
		NextHandler: handler,
	}
	server := httptest.NewServer(errorHandler)

	_, err := http.Post(server.URL, "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, errorHandler.Body) {
		t.Fatalf("expected %v, but got %v", payload, errorHandler.Body)
	}
	if panicError != errorHandler.Error {
		t.Fatalf("expected %v, but got %v", panicError, errorHandler.Error)
	}
}

func TestNestedDoppelganger(t *testing.T) {
	payload := []byte("Hello World")
	factory := doppelgangerreader.NewFactory(bytes.NewReader(payload))
	secondFactory := doppelgangerreader.NewFactory(factory.NewDoppelganger())
	if factory == secondFactory {
		t.Fatalf("expected not %v, but got %v", factory, secondFactory)
	}

	// test if we can read from new factory without error
	b, err := ioutil.ReadAll(io.LimitReader(secondFactory.NewDoppelganger(), 1))
	if err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}

	if !bytes.Equal(payload[:1], b) {
		t.Fatalf("expected %v, but got %v", payload, b)
	}

	if err := secondFactory.Close(); err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}

	// test if we can still read from original factory without error
	b, err = ioutil.ReadAll(factory.NewDoppelganger())
	if err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}
	if !bytes.Equal(payload, b) {
		t.Fatalf("expected %v, but got %v", payload, b)
	}

	if err := factory.Close(); err != nil {
		t.Fatalf("expected no error, but got %v", err)
	}
}

func TestHTTPMiddleware(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if doppelgangerreader.HTTPBodyFactory(r) == nil {
			t.Fatal("no body factory found")
		}
		_, _ = w.Write([]byte{'O', 'K'})
	})
	srv := httptest.NewServer(doppelgangerreader.HTTPMiddleware(mux, 0))
	defer srv.Close()

	response, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("unable to get response: %v", err)
	}

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected %v, but got %v", http.StatusOK, response.StatusCode)
	}
	defer response.Body.Close()
	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("unable to get body: %v", err)
	}

	if !bytes.Equal(body, []byte{'O', 'K'}) {
		t.Fatalf("expected %v, but got %v", []byte{'O', 'K'}, body)
	}
}

func TestReaderInstanceCloseIsIdempotent(t *testing.T) {
	// Closing a doppelganger twice must not panic. RemoveDoppelganger and a
	// first Close both detach the reader from its factory, and Close used to
	// dereference that pointer unconditionally.
	//
	// The factory is deliberately left open (the source is never read to EOF),
	// because a closed factory takes a different branch and hides the bug.
	factory := doppelgangerreader.NewFactory(bytes.NewReader(make([]byte, 128)))
	defer factory.Close()

	reader := factory.NewDoppelganger()
	if _, err := reader.Read(make([]byte, 8)); err != nil {
		t.Fatalf("partial read: %v", err)
	}

	if err := reader.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestReaderInstanceCloseAfterRemove(t *testing.T) {
	// Same hazard reached the other way round: RemoveDoppelganger detaches the
	// reader, then Close is called on it.
	factory := doppelgangerreader.NewFactory(bytes.NewReader(make([]byte, 128)))
	defer factory.Close()

	reader := factory.NewDoppelganger()
	if _, err := reader.Read(make([]byte, 8)); err != nil {
		t.Fatalf("partial read: %v", err)
	}

	if err := factory.RemoveDoppelganger(reader); err != nil {
		t.Fatalf("RemoveDoppelganger: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close after RemoveDoppelganger: %v", err)
	}
}

func TestConcurrentCloseAndRead(t *testing.T) {
	// Close and Read both inspect state owned by the factory. Close used to
	// read closedOn without the mutex, and RemoveDoppelganger used to clear
	// DoppelBase while Read was reading it, so the race detector reported two
	// distinct races here.
	factory := doppelgangerreader.NewFactory(bytes.NewReader(make([]byte, 1<<16)))
	defer factory.Close()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		reader := factory.NewDoppelganger()
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = ioutil.ReadAll(reader)
		}()
		go func() {
			defer wg.Done()
			_ = reader.Close()
		}()
	}
	wg.Wait()
}

func TestConcurrentCloseAndFactoryClose(t *testing.T) {
	// Factory.Close writes closedOn while the per-reader Close reads it.
	factory := doppelgangerreader.NewFactory(bytes.NewReader(make([]byte, 1<<16)))

	readers := make([]io.ReadCloser, 0, 16)
	for i := 0; i < 16; i++ {
		readers = append(readers, factory.NewDoppelganger())
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = factory.Close()
	}()
	for _, r := range readers {
		wg.Add(1)
		go func(r io.ReadCloser) {
			defer wg.Done()
			_ = r.Close()
		}(r)
	}
	wg.Wait()
}

func TestConcurrentRemoveAndRead(t *testing.T) {
	// RemoveDoppelganger detaches a reader that another goroutine may be
	// reading. The reader must stop cleanly at io.EOF rather than racing.
	factory := doppelgangerreader.NewFactory(bytes.NewReader(make([]byte, 1<<16)))
	defer factory.Close()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		reader := factory.NewDoppelganger()
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = ioutil.ReadAll(reader)
		}()
		go func() {
			defer wg.Done()
			_ = factory.RemoveDoppelganger(reader)
		}()
	}
	wg.Wait()
}

func TestReadAfterRemoveReturnsEOF(t *testing.T) {
	// Detaching a reader must end its stream. This used to work by clearing
	// DoppelBase; it is now a flag checked under the lock, so pin the
	// behaviour.
	factory := doppelgangerreader.NewFactory(bytes.NewReader(make([]byte, 128)))
	defer factory.Close()

	reader := factory.NewDoppelganger()
	if _, err := reader.Read(make([]byte, 8)); err != nil {
		t.Fatalf("partial read: %v", err)
	}
	if err := factory.RemoveDoppelganger(reader); err != nil {
		t.Fatalf("RemoveDoppelganger: %v", err)
	}

	if _, err := reader.Read(make([]byte, 8)); err != io.EOF {
		t.Fatalf("expected io.EOF after removal, got %v", err)
	}
}

func TestGetFactoryAfterRemove(t *testing.T) {
	// GetFactory reports the factory for a doppelganger. A detached reader is
	// no longer attached to anything the caller can usefully nest onto, so it
	// must not hand back a live factory.
	//
	// This previously worked as a side effect of RemoveDoppelganger clearing
	// DoppelBase. That field is now left intact, so the behaviour has to be
	// preserved deliberately.
	factory := doppelgangerreader.NewFactory(bytes.NewReader(make([]byte, 128)))
	defer factory.Close()

	reader := factory.NewDoppelganger()
	if doppelgangerreader.GetFactory(reader) == nil {
		t.Fatal("expected a factory for an attached doppelganger")
	}

	if err := factory.RemoveDoppelganger(reader); err != nil {
		t.Fatalf("RemoveDoppelganger: %v", err)
	}
	if got := doppelgangerreader.GetFactory(reader); got != nil {
		t.Errorf("expected nil factory for a detached doppelganger, got %T", got)
	}
}

func TestHTTPMiddlewareBodyWithinLimit(t *testing.T) {
	// A body at or below the limit must be delivered whole, with no error.
	for _, size := range []int{0, 1, 7, 8} {
		const limit = 8
		body := bytes.Repeat([]byte("A"), size)

		var got []byte
		var readErr error
		handler := doppelgangerreader.HTTPMiddleware(http.HandlerFunc(
			func(_ http.ResponseWriter, r *http.Request) {
				got, readErr = ioutil.ReadAll(r.Body)
			}), limit)

		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		handler.ServeHTTP(httptest.NewRecorder(), req)

		if readErr != nil {
			t.Fatalf("size %d: unexpected error: %v", size, readErr)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("size %d: got %q, want %q", size, got, body)
		}
	}
}

func TestHTTPMiddlewareBodyOverLimitErrors(t *testing.T) {
	// A body over the limit must surface an error rather than arriving
	// truncated. Silent truncation is dangerous for an authenticated body: the
	// handler would verify a signature over bytes the client never sent and
	// have no way to notice.
	const limit = 8
	body := bytes.Repeat([]byte("A"), 32)

	var readErr error
	handler := doppelgangerreader.HTTPMiddleware(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			_, readErr = ioutil.ReadAll(r.Body)
		}), limit)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatal("expected an error for a body over the limit, got nil")
	}
	if !doppelgangerreader.IsBodyTooLargeError(readErr) {
		t.Fatalf("expected a BodyTooLargeError, got %T: %v", readErr, readErr)
	}
}

func TestHTTPMiddlewareOverLimitViaFactory(t *testing.T) {
	// The error must also reach a reader taken from the factory, not just the
	// one installed as r.Body.
	const limit = 4
	body := bytes.Repeat([]byte("B"), 64)

	var readErr error
	handler := doppelgangerreader.HTTPMiddleware(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			factory := doppelgangerreader.HTTPBodyFactory(r)
			if factory == nil {
				t.Error("no factory on the request")
				return
			}
			d := factory.NewDoppelganger()
			defer d.Close()
			_, readErr = ioutil.ReadAll(d)
		}), limit)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !doppelgangerreader.IsBodyTooLargeError(readErr) {
		t.Fatalf("expected a BodyTooLargeError, got %T: %v", readErr, readErr)
	}
}

func TestHTTPMiddlewareNoLimit(t *testing.T) {
	// limit 0 keeps the previous unlimited behaviour.
	body := bytes.Repeat([]byte("C"), 1<<16)

	var got []byte
	var readErr error
	handler := doppelgangerreader.HTTPMiddleware(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			got, readErr = ioutil.ReadAll(r.Body)
		}), 0)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if readErr != nil {
		t.Fatalf("unexpected error: %v", readErr)
	}
	if len(got) != len(body) {
		t.Fatalf("got %d bytes, want %d", len(got), len(body))
	}
}

func TestIsBodyTooLargeError(t *testing.T) {
	if !doppelgangerreader.IsBodyTooLargeError(doppelgangerreader.BodyTooLargeError{Limit: 1}) {
		t.Error("expected true for a BodyTooLargeError")
	}
	if doppelgangerreader.IsBodyTooLargeError(errors.New("other")) {
		t.Error("expected false for an unrelated error")
	}
	if doppelgangerreader.IsBodyTooLargeError(nil) {
		t.Error("expected false for nil")
	}
}

func TestBodyTooLargeErrorMatching(t *testing.T) {
	// The error has to be matchable without the caller knowing the limit.
	// Without an Is method errors.Is falls back to struct equality, so
	// errors.Is(err, BodyTooLargeError{}) would be false for any non-zero
	// limit - which is the obvious way to write the check.
	const limit = 8

	var readErr error
	handler := doppelgangerreader.HTTPMiddleware(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			_, readErr = ioutil.ReadAll(r.Body)
		}), limit)

	req := httptest.NewRequest(http.MethodPost, "/",
		bytes.NewReader(bytes.Repeat([]byte("A"), 32)))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatal("expected an error for an over-limit body")
	}

	// The zero value must match regardless of the configured limit.
	if !errors.Is(readErr, doppelgangerreader.BodyTooLargeError{}) {
		t.Error("errors.Is failed against the zero-value target")
	}
	// A populated target must match too.
	if !errors.Is(readErr, doppelgangerreader.BodyTooLargeError{Limit: limit}) {
		t.Error("errors.Is failed against a populated target")
	}
	// Even one carrying a different limit: the limit is data, not identity.
	if !errors.Is(readErr, doppelgangerreader.BodyTooLargeError{Limit: 999}) {
		t.Error("errors.Is failed against a target with a different limit")
	}

	// errors.As recovers the limit.
	var target doppelgangerreader.BodyTooLargeError
	if !errors.As(readErr, &target) {
		t.Fatal("errors.As could not recover BodyTooLargeError")
	}
	if target.Limit != limit {
		t.Errorf("recovered Limit = %d, want %d", target.Limit, limit)
	}

	// All of it must survive wrapping.
	wrapped := fmt.Errorf("reading body: %w", readErr)
	if !errors.Is(wrapped, doppelgangerreader.BodyTooLargeError{}) {
		t.Error("errors.Is failed on a wrapped error")
	}
	if !doppelgangerreader.IsBodyTooLargeError(wrapped) {
		t.Error("IsBodyTooLargeError failed on a wrapped error")
	}
	var wrappedTarget doppelgangerreader.BodyTooLargeError
	if !errors.As(wrapped, &wrappedTarget) || wrappedTarget.Limit != limit {
		t.Error("errors.As failed to recover the limit from a wrapped error")
	}

	// It must not match something unrelated.
	if errors.Is(readErr, doppelgangerreader.NilReaderError{}) {
		t.Error("a BodyTooLargeError matched NilReaderError")
	}
}

func TestNilReaderErrorMatching(t *testing.T) {
	// NilReaderError predates this change; its Is* helper used a type
	// assertion, so it returned false for a wrapped error even though
	// errors.Is and errors.As both matched.
	factory := doppelgangerreader.NewFactory(nil)
	reader := factory.NewDoppelganger()

	_, err := reader.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected an error from a nil source")
	}

	if !errors.Is(err, doppelgangerreader.NilReaderError{}) {
		t.Error("errors.Is failed on the bare error")
	}
	if !doppelgangerreader.IsNilReaderError(err) {
		t.Error("IsNilReaderError failed on the bare error")
	}

	wrapped := fmt.Errorf("outer: %w", err)
	if !errors.Is(wrapped, doppelgangerreader.NilReaderError{}) {
		t.Error("errors.Is failed on a wrapped error")
	}
	if !doppelgangerreader.IsNilReaderError(wrapped) {
		t.Error("IsNilReaderError failed on a wrapped error")
	}

	var target doppelgangerreader.NilReaderError
	if !errors.As(wrapped, &target) {
		t.Error("errors.As failed on a wrapped error")
	}
}
