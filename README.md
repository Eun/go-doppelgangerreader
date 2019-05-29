# DoppelgangerReader



DoppelgangerReader provides a way to read one `io.Reader` multiple times.


```go
package main

import (
	"fmt"
	"bytes"
	"io/ioutil"
	"github.com/Eun/go-doppelgangerreader"
)

func main() {
	reader := bytes.NewBufferString("Hello World")
	factory := doppelgangerreader.NewFactory(reader)
	defer factory.Close()

	d1 := factory.NewDoppelganger()
	defer d1.Close()

	fmt.Println(ioutil.ReadAll(d1))

	d2 := factory.NewDoppelganger()
	defer d2.Close()

	fmt.Println(ioutil.ReadAll(d2))
}
```