package doppelgangerreader

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"io/ioutil"
	"math/big"
	"sync"
	"testing"
)

func ReadAtLeast(t *testing.T, r io.Reader, size int) []byte {
	buf := make([]byte, size)
	n, err := io.ReadAtLeast(r, buf, size)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}

func Read(t *testing.T, r io.Reader, size int) []byte {
	buf := make([]byte, size)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}

func TestDoppelganger(t *testing.T) {
	reader := NewFactory(rand.Reader)
	defer reader.Close()

	reader1 := reader.NewDoppelganger()
	// read 10
	buf1 := ReadAtLeast(t, reader1, 10)

	// create a new reader and read 20
	reader2 := reader.NewDoppelganger()
	buf2 := ReadAtLeast(t, reader2, 20)

	// the first 10 bytes should be equal
	if !bytes.Equal(buf1, buf2[:10]) {
		t.Fatalf("expected %v, but got %v", buf1, buf2[:10])
	}

	// read more on reader1
	buf1 = ReadAtLeast(t, reader1, 10)
	if !bytes.Equal(buf1, buf2[10:]) {
		t.Fatalf("expected %v, but got %v", buf1, buf2[10:])
	}

	// read more on reader2
	buf2 = ReadAtLeast(t, reader2, 10)
	buf1 = ReadAtLeast(t, reader1, 10)
	if !bytes.Equal(buf2, buf1) {
		t.Fatalf("expected %v, but got %v", buf2, buf1)
	}
}

func TestReaderInstanceClose(t *testing.T) {
	reader := NewFactory(rand.Reader)
	defer reader.Close()

	reader1 := reader.NewDoppelganger()
	reader2 := reader.NewDoppelganger()
	buf1 := ReadAtLeast(t, reader1, 10)
	reader1.Close()

	buf2 := ReadAtLeast(t, reader2, 10)

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

	reader := NewFactory(r)
	defer reader.Close()

	reader1 := reader.NewDoppelganger()
	buf1 := Read(t, reader1, 10)
	n, err := reader1.Read(buf1)
	if err != io.EOF {
		t.Fatalf("expected io.EOF, but got %T", err)
	}
	if n != 0 {
		t.Fatalf("expected 0, but got %d", n)
	}

	// do it again with another reader, we should expect the same behaviour
	reader2 := reader.NewDoppelganger()
	buf2 := Read(t, reader2, 10)
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
	factory := NewFactory(dummy)
	if err := factory.Close(); err != nil {
		t.Fatal("expected no error")
	}
	if dummy.closed {
		t.Fatal("expected dummy not to be closed")
	}
}

func TestDoppelganger_RemoveReader(t *testing.T) {
	t.Run("invalid reader", func(t *testing.T) {
		reader := NewFactory(rand.Reader)
		defer reader.Close()

		if err := reader.RemoveDoppelganger(ioutil.NopCloser(bytes.NewBuffer(nil))); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("already removed reader", func(t *testing.T) {
		reader := NewFactory(rand.Reader)
		defer reader.Close()

		r1 := reader.NewDoppelganger()
		if err := reader.RemoveDoppelganger(r1); err != nil {
			t.Fatal("expected no error")
		}

		if err := reader.RemoveDoppelganger(r1); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestConcurrent(t *testing.T) {
	factory := NewFactory(rand.Reader)

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
			t.Fatalf("expected no error %v", key)
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
	factory := NewFactory(bytes.NewReader(nil))
	factory.buffer.WriteString("Hello World")
	if err := factory.Close(); err != nil {
		t.Fatalf("expected no error")
	}
	r := factory.NewDoppelganger()
	buf, err := ioutil.ReadAll(r)
	if err != nil {
		t.Fatalf("expected no error")
	}
	if !bytes.Equal([]byte("Hello World"), buf) {
		t.Fatalf("expected %v, but got %v", []byte("Hello World"), buf)
	}
}
