package doppelgangerreader_test

import (
	"bytes"
	"crypto/rand"
	"errors"
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
