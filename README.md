# DoppelgangerReader
[![CI](https://github.com/Eun/go-doppelgangerreader/actions/workflows/ci.yml/badge.svg)](https://github.com/Eun/go-doppelgangerreader/actions/workflows/ci.yml)
[![PkgGoDev](https://img.shields.io/badge/pkg.go.dev-reference-blue)](https://pkg.go.dev/github.com/Eun/go-doppelgangerreader)
[![go-report](https://goreportcard.com/badge/github.com/Eun/go-doppelgangerreader)](https://goreportcard.com/report/github.com/Eun/go-doppelgangerreader)
---
DoppelgangerReader provides a way to read one `io.Reader` multiple times.

An `io.Reader` can normally only be consumed once. A `DoppelgangerFactory`
wraps a reader and hands out any number of independent readers over the same
bytes, buffering only as much as it needs to keep them in sync.

## Install
```bash
go get github.com/Eun/go-doppelgangerreader
```

## Usage
```go
package main

import (
	"bytes"
	"fmt"
	"io"

	"github.com/Eun/go-doppelgangerreader"
)

func main() {
	reader := bytes.NewBufferString("Hello World")
	factory := doppelgangerreader.NewFactory(reader)
	defer factory.Close()

	d1 := factory.NewDoppelganger()
	defer d1.Close()
	fmt.Println(io.ReadAll(d1))

	d2 := factory.NewDoppelganger()
	defer d2.Close()
	fmt.Println(io.ReadAll(d2))
}
```

Readers are independent: each one sees the full stream regardless of what the
others have already consumed, and in any order.

## HTTP middleware
The common case is an HTTP body that has to be read more than once — for
example verifying a signature over the raw bytes while also decoding it as
JSON. `HTTPMiddleware` installs a factory on the request and replaces the body
with a doppelganger:

```go
handler := doppelgangerreader.HTTPMiddleware(next, 1<<20) // 1 MiB limit, 0 = unlimited

func next(w http.ResponseWriter, r *http.Request) {
	factory := doppelgangerreader.HTTPBodyFactory(r)

	// the raw bytes, for a signature check
	raw := factory.NewDoppelganger()
	defer raw.Close()

	// r.Body is still readable, for the usual decoding
	var payload map[string]any
	_ = json.NewDecoder(r.Body).Decode(&payload)
}
```

A body larger than the limit is reported as `BodyTooLargeError` on the next
read rather than being silently truncated, so a handler can never mistake a
truncated body for a complete one.

## Errors
Every error is a concrete type, and all of them work with `errors.Is` and
`errors.As`, including through wrapping:

| Error | Reported when |
|---|---|
| `NilReaderError` | the reader to mimic is `nil` |
| `ReaderNotFoundError` | the reader is not (or no longer) registered with the factory |
| `NotAReaderInstanceError` | the reader was not created by a `DoppelgangerFactory` |
| `BodyTooLargeError` | the HTTP body exceeded the `HTTPMiddleware` limit |

```go
if errors.Is(err, doppelgangerreader.BodyTooLargeError{}) {
	// ...
}

// errors.As when you need the detail
var tooLarge doppelgangerreader.BodyTooLargeError
if errors.As(err, &tooLarge) {
	log.Printf("limit was %d bytes", tooLarge.Limit)
}
```

Each also has an `Is…Error(err)` helper (`IsNilReaderError`, and so on) for
callers who prefer that style; they are built on `errors.As`, so they match
wrapped errors too.

## Development
```bash
mise install          # install the pinned Go and golangci-lint
mise run test         # tests with coverage -> coverage.out
mise run test:race    # tests under the race detector
mise run lint         # golangci-lint
```

### Prerequisites
- [mise](https://mise.jdx.dev/) — manages tool versions and tasks

Tool versions are pinned in `mise.toml`.

### Mise tasks
| Task | Description |
|---|---|
| `mise run test` | Run tests with coverage |
| `mise run test:race` | Run tests with the race detector |
| `mise run lint` | Run golangci-lint |
| `mise run clean` | Remove build artifacts |

### CI
CI logic is driven by mise — the GitHub Actions workflows only check out,
install mise, and run tasks.

| Workflow | Trigger | What it does |
|---|---|---|
| `ci.yml` | Push + PR | Lint, vulnerability scan (nancy), tests with coverage on Linux/macOS/Windows, race detector, coverage PR comment |
| `pr_labeler.yml` | PR opened/edited | Labels PRs from their title |
| `release_draft.yml` | Push to default branch | Drafts release notes |

The race detector runs as its own job because the package hands out concurrent
readers over a single source, so it is part of the contract rather than an
optional extra.
