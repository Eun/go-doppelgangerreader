package doppelgangerreader_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"net/http"

	"io"
	"log"

	"github.com/Eun/go-doppelgangerreader"
)

func ExampleDoppelgangerFactory_NewDoppelganger() {
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

func ExampleDoppelgangerFactory_NewDoppelganger_httpResponse() {
	res := &http.Response{
		Body: ioutil.NopCloser(bytes.NewBufferString("Hello World")),
	}

	factory := doppelgangerreader.NewFactory(res.Body)
	defer factory.Close()

	var jsonObject map[string]interface{}
	if err := json.NewDecoder(factory.NewDoppelganger()).Decode(&jsonObject); err == nil {
		fmt.Printf("Body is a JSON Object: %+v", jsonObject)
		return
	}

	var jsonArray []interface{}
	if err := json.NewDecoder(factory.NewDoppelganger()).Decode(&jsonArray); err == nil {
		fmt.Printf("Body is a JSON Array: %+v", jsonArray)
		return
	}

	var xmlObject map[string]interface{}
	if err := xml.NewDecoder(factory.NewDoppelganger()).Decode(&xmlObject); err == nil {
		fmt.Printf("Body is a XML Object: %+v", xmlObject)
		return
	}

	var xmlArray []interface{}
	if err := xml.NewDecoder(factory.NewDoppelganger()).Decode(&xmlArray); err == nil {
		fmt.Printf("Body is a XML Array: %+v", xmlArray)
		return
	}
}

type errorHandler struct {
	NextHandler http.Handler
}

func (e errorHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	factory := doppelgangerreader.NewFactory(request.Body)
	request.Body = factory.NewDoppelganger()
	defer func() {
		err := recover()
		body, _ := ioutil.ReadAll(io.LimitReader(factory.NewDoppelganger(), 128))
		log.Printf("handler panic: %#v, body was %v", err, body)
		factory.Close()
	}()
	e.NextHandler.ServeHTTP(writer, request)
}

func ExampleDoppelgangerFactory_httpErrorHandler() {
	handler := http.NewServeMux()
	handler.HandleFunc("/", func(w http.ResponseWriter, request *http.Request) {
		_, _ = ioutil.ReadAll(request.Body)
		panic("some random error")
	})

	http.ListenAndServe(":8000", errorHandler{handler})
}
