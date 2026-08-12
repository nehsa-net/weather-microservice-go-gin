# Weather Microservice — Go + Gin

A small HTTP service that fetches current conditions from the OpenWeatherMap API and returns them as JSON, a temperature, or a one-word description.

**This README doubles as a standalone Go reference.** Everything from ["Go reference"](#go-reference) down is general — it is written to be copied into OneNote and read on a machine with no internet and no AI. It assumes no prior Go knowledge.

---

## Part 1 — This service

### What it does

| Endpoint | Returns | Example |
|---|---|---|
| `GET /weather_all` | The full OpenWeatherMap payload, re-serialised as JSON | `curl "http://localhost:8080/weather_all?city=Vancouver&units=metric"` |
| `GET /weather_temp` | Just the temperature, 2 decimal places, as plain text | `curl "http://localhost:8080/weather_temp?city=Vancouver&units=imperial"` |
| `GET /weather_description` | One word — `Clouds`, `Rain`, `Clear` … | `curl "http://localhost:8080/weather_description?city=Vancouver"` |

**Query parameters** (both optional on every endpoint):

- `city` — `"City"`, `"City, State"`, or `"City, State, Country"`. Defaults to `Cape Canaveral, FL` (`weather_all` defaults to `Cape Canaveral, FL, United States`).
- `units` — `imperial` (default), `metric`, or `standard` (Kelvin).

> **The parameter is `units`, not `unit`**, and multiple parameters are joined with `&`, not `?`. Earlier revisions of this file documented `?city=X?unit=Y`, which silently falls back to the defaults because the whole string after the second `?` is parsed as part of the city value.

### Configuration — an API key is required

The service reads the OpenWeatherMap key from the **`OPENWEATHER_API_KEY`**
environment variable:

```bash
export OPENWEATHER_API_KEY="your-openweathermap-api-key"
```

Get a free key at <https://openweathermap.org/api>. **The service refuses to
start without one**, and says which variable is missing — rather than starting
happily and turning every request into a confusing upstream 401.

For backwards compatibility it still falls back to a file named
**`openweatherapi.key`** in the working directory, first line only, if the
variable is unset. The environment wins when both are present.

`openweatherapi.key` must never be committed. Confirm it is ignored:

```bash
git check-ignore -v openweatherapi.key   # should print a .gitignore line
```

**Other variables**, all optional:

| Variable | Default | What it does |
|---|---|---|
| `ADDR` | `:8080` | Listen address |
| `WEATHER_UPSTREAM_URL` | `https://api.openweathermap.org` | The upstream base URL. A field rather than a constant so tests can point it at a stub |

### Run it

```bash
cd weather-microservice-go-gin
go mod download                 # fetch dependencies into the module cache
go run .                        # starts on :8080
```

Use `go run .` — it builds and runs the whole package. The entry point is `main.go`, which does nothing but read config, wire dependencies and serve; everything worth testing lives in `internal/`.

### Run it in Docker

```bash
docker build . -t weather-microservice
docker run --rm -p 8080:8080 \
  -e OPENWEATHER_API_KEY="$OPENWEATHER_API_KEY" \
  weather-microservice
```

The Dockerfile is a multi-stage build onto `distroless/static`, so the runtime
image carries the binary and nothing else — no shell, no toolchain, no source,
and no possibility of a key file being baked in. It sets `GIN_MODE=release` and
runs as `nonroot`.

### Publish and deploy

- `publish.ps1` builds a `linux/amd64` image tagged with a UTC-ish timestamp and pushes it to Docker Hub as `nehsa/ascii-weather`.
- `weather-microservice.yaml` is a Kubernetes Deployment for `nehsa/ascii-weather:latest`, one replica, container port 8080.

### Endpoints for Kubernetes

| Endpoint | Returns |
|---|---|
| `GET /health` | `{"status":"ok"}` — the liveness probe |
| `GET /ready` | `{"status":"ready"}` — the readiness probe |

Both answer **without touching OpenWeatherMap**. That is deliberate: a probe
that depends on a third party takes every pod out of rotation during somebody
else's outage.

### Layout

```
main.go                    reads config, wires dependencies, serves — nothing else
internal/weather/
  model.go                 pure values, parsing, conversion       (unit)
  client.go                the ONE place that does network I/O    (unit + integration)
  service.go               orchestration over an interface        (unit)
internal/httpapi/router.go gin routes, status codes, query parsing (unit)
internal/config/config.go  environment and key-file resolution     (unit)
test/integration/          //go:build integration
test/e2e/                  //go:build e2e
```

### Testing this service

```bash
make test              # unit tier — 83 cases, no network, no setup
make test-integration  # the assembled stack against a stubbed upstream
make test-e2e          # builds the binary and drives it over HTTP
make test-all          # all three, fastest failure first
make cover             # coverage summary
make ci                # the full gate — run this before every commit
```

103 tests pass in total: 83 unit, 10 integration, 10 e2e. Unit statement
coverage is 76.2%; the uncovered remainder is `main.go`, which the e2e tier
covers as a process.

**GitHub Actions is disabled on this repository**, so nothing runs on push and
`make ci` is the only gate. Run it before every commit. The workflow file is
kept as reference and has been verified by running it in a real runner
container with [`act`](https://github.com/nektos/act); it is simply not
switched on.

**The pattern is documented in full at `github.com/nehsa-net/test-go`** — this
service follows it, and that repo explains why each tier exists and what each
one can prove that the others cannot.

### What changed to make this testable

The service previously had no tests, and could not have had useful ones: the
code that made decisions and the code that did I/O were the same code. Four
seams were introduced.

1. **The module path.** It was `module main`, which nothing can import — so no
   test file could reach any of it. It is now
   `github.com/nehsa-net/weather-microservice-go-gin`.
2. **The upstream URL is a field, not a constant.** `Client.BaseURL` is the
   single change that makes the integration and e2e tiers possible: a test
   points it at an `httptest.Server`.
3. **The HTTP client is an interface.** `weather.Doer` is one method wide, so a
   test can inject a stub — and production passes `*http.Client` unchanged.
4. **Handlers are named functions taking a service.** They were closures inside
   `main()`, where no test could reach them.

Five real defects were fixed on the way, each with a regression test:

- **A panic on any error payload.** `weatherData.Weather[0]` was indexed
  unchecked, so an empty `weather` array took the process down.
- **The API key leaked to callers.** Handlers returned `err.Error()` verbatim,
  and the wrapped error carried the full request URL — `appid` included — on
  any transport failure. Errors now map to a status code and a flat sentence,
  with the cause going to the log.
- **The key was printed to stdout on every request**, putting a live credential
  in the container logs, and re-read from disk each time.
- **A shadowed `err` in `getWeatherJson`** meant a fetch failure was overwritten
  by the marshalling result and never checked.
- **No timeout on the upstream call.** A hung upstream held the request open
  indefinitely.

And two things outside the Go code:

- **`/health` and `/ready` did not exist**, though the Kubernetes manifest has
  probed them since it was written — so liveness 404'd and would have restarted
  the pod in a loop.
- **`weather-microservice.yaml` was not valid YAML.** The indentation put
  `labels`, `app` and the container block at the wrong depth; `kubectl apply`
  would have rejected it.

## Part 2 — Go reference

Everything below is general-purpose Go. It is deliberately self-contained.

> Written against **Go 1.24+**, the version this module targets (`go 1.24` in `go.mod`). Go is exceptionally stable — the Go 1 compatibility promise means code written for 1.0 in 2012 still compiles today — so almost all of this stays true across versions. Version-specific notes are called out inline.

### What Go is, and when to reach for it

Go is a statically typed, compiled language from Google, designed around a small feature set and fast builds. Its distinguishing traits:

- **Compiles to a single static binary** with no runtime dependency. You copy one file to a server and run it. This is why Go dominates containers and CLI tools — a `FROM scratch` image can be ~10 MB.
- **Concurrency is a language feature**, not a library. Goroutines cost ~2 KB of stack, so hundreds of thousands are routine.
- **The standard library is genuinely batteries-included** — a production-grade HTTP server, TLS, JSON, templating, and crypto ship with the toolchain.
- **One formatting style, enforced by tooling.** `gofmt` ends all formatting debate. Nobody argues about braces in Go code review.
- **Deliberately small.** No inheritance, no exceptions, no operator overloading. Generics arrived only in 1.18, and are used sparingly.

The trade: Go is verbose. You will write `if err != nil` thousands of times. That is a considered choice — errors are values you handle, not control flow that jumps.

**Good fits:** network services, CLI tools, infrastructure, anything concurrent, anything you want to ship as one binary.
**Poor fits:** heavy numeric/scientific work, GUI applications, anything needing a rich generic type system.

### Installing Go

**Check whether it is already there:**

```bash
go version        # -> go version go1.22.6 linux/amd64
```

**Linux** — do *not* use `apt install golang`; distro packages are usually years old.

```bash
# 1. Download from https://go.dev/dl/  (pick linux-amd64)
curl -LO https://go.dev/dl/go1.22.6.linux-amd64.tar.gz

# 2. Remove any previous install, then extract into /usr/local
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.22.6.linux-amd64.tar.gz

# 3. Put it on PATH — add to ~/.bashrc or ~/.zshrc
export PATH=$PATH:/usr/local/go/bin
export PATH=$PATH:$(go env GOPATH)/bin    # so `go install`ed tools are runnable

# 4. Reload and verify
source ~/.bashrc
go version
```

**macOS:** `brew install go`, or the official `.pkg` from <https://go.dev/dl/>.

**Windows:** the `.msi` from <https://go.dev/dl/> sets `PATH` for you. Verify in a *new* terminal — the installer does not update already-open shells.

**Multiple versions**, when you need them:

```bash
go install golang.org/dl/go1.21.13@latest
go1.21.13 download
go1.21.13 version      # this specific version, alongside your main one
```

### The environment, demystified

Three names cause most of the early confusion:

| Name | What it is | Do you set it? |
|---|---|---|
| `GOROOT` | Where Go itself is installed (`/usr/local/go`) | **No.** The toolchain knows. Setting it wrongly breaks everything. |
| `GOPATH` | Where downloaded modules and `go install` binaries land (default `~/go`) | Rarely. The default is fine. |
| `GOBIN` | Where `go install` puts binaries (default `$GOPATH/bin`) | Only if you want them elsewhere. |

Since **modules** (Go 1.11+, mandatory since 1.16), **your code does not live in `GOPATH`.** Put your project anywhere. Any advice telling you to work inside `~/go/src/github.com/you/project` predates modules and is obsolete.

Inspect everything with `go env`. Set a value persistently with `go env -w GOPROXY=...`.

### The toolchain — the commands you actually use

```bash
go run .              # compile and run the current package (no binary left behind)
go run ./cmd/server   # run a specific package
go build              # compile -> binary named after the module/directory
go build -o app .     # compile -> ./app
go install ./...      # compile and place binaries in $GOPATH/bin
go test ./...         # run every test in the module
go fmt ./...          # format (wraps gofmt) — run it, always
go vet ./...          # static checks for likely bugs — run it, always
go mod init example.com/mymodule   # start a module (creates go.mod)
go mod tidy           # add missing deps, drop unused ones — run before committing
go mod download       # populate the module cache without building
go get example.com/pkg@v1.2.3      # add or upgrade a dependency
go clean -modcache    # nuke the module cache when it is corrupt
go doc net/http       # read docs offline
go doc net/http.Get   # docs for one symbol
```

`./...` means "this directory and everything under it" — the standard way to say "the whole project".

**`go get` vs `go install`** — this changed in Go 1.16 and old blog posts get it wrong:
- `go get` — manages **dependencies** of the current module. Edits `go.mod`.
- `go install pkg@version` — installs an **executable tool**. Does not touch `go.mod`.

### Modules and project layout

A module is a directory tree with a `go.mod` at its root.

```
myproject/
├── go.mod              # module path, Go version, dependencies
├── go.sum              # cryptographic checksums of dependencies — commit it
├── main.go             # package main
├── internal/           # importable ONLY from within this module (compiler-enforced)
│   └── store/
│       └── store.go    # package store
├── pkg/                # optional: code you intend others to import
└── cmd/
    ├── server/main.go  # multiple binaries live here
    └── worker/main.go
```

`go.mod` for this repo:

```
module github.com/nehsa-net/weather-microservice-go-gin
go 1.24
require github.com/gin-gonic/gin v1.10.0
```

> **Name your module after its import path** — `github.com/nehsa-net/weather-microservice-go-gin`, not `main`. A module named `main` cannot be imported by anything, which is fine for a leaf binary and a wall the moment you want to share a package or split out `cmd/`. Both repos here do this; harmless today, worth fixing before either grows.

**`internal/` is a real language feature**, not a convention: the compiler refuses imports of `internal/...` from outside the parent module. It is the only true encapsulation boundary Go has above the package level. Use it freely.

**Package rules:**
- A directory is a package. The package name usually matches the directory.
- `package main` with `func main()` produces an executable. Anything else produces a library.
- **Capitalisation is the access modifier.** `Exported` is public, `unexported` is package-private. There is no `public`/`private` keyword.
- Import cycles are a **compile error**. Go will not let you write them, which forces a clean dependency graph.

### Language essentials

```go
// Variables
var x int = 5      // explicit
var y = 5          // type inferred
z := 5             // short form — only inside functions
const Pi = 3.14159 // constants: numbers, strings, booleans only

// Zero values — every type has one, and it is always usable.
var i int        // 0
var s string     // ""
var b bool       // false
var p *int       // nil
var sl []int     // nil, but len/append work on it
var m map[string]int  // nil — reads OK, WRITES PANIC
```

The zero value is central to Go's design: `var buf bytes.Buffer` is immediately usable without a constructor. Design your own types so their zero value works too.

```go
// Functions — multiple returns are idiomatic, and how errors travel
func divide(a, b float64) (float64, error) {
    if b == 0 {
        return 0, fmt.Errorf("divide by zero")
    }
    return a / b, nil
}

result, err := divide(10, 2)
if err != nil {
    return err
}
```

```go
// Slices — the workhorse. A view over an array: pointer, length, capacity.
s := []int{1, 2, 3}
s = append(s, 4)         // append RETURNS a new slice — you must reassign
sub := s[1:3]            // [2 3] — SHARES the same backing array
fmt.Println(len(s), cap(s))
c := make([]int, 0, 100) // preallocate capacity you know you need

// Maps
m := map[string]int{"a": 1}
v, ok := m["b"]          // ok is false when absent; v is the zero value
delete(m, "a")
// Map iteration order is RANDOMISED on purpose. Sort keys if you need order.

// Structs
type User struct {
    Name  string `json:"name"`   // struct tags drive encoding/json
    Email string `json:"email,omitempty"`
    age   int                    // unexported: invisible to other packages AND to json
}
u := User{Name: "Ada", Email: "ada@example.com"}
```

```go
// Methods — on any named type, not just structs
type Celsius float64
func (c Celsius) Fahrenheit() float64 { return float64(c)*9/5 + 32 }

// Pointer receiver when you need to MUTATE or the struct is large
func (u *User) SetName(n string) { u.Name = n }
```

**Pick one receiver kind per type and stick to it.** Mixing value and pointer receivers on the same type is a classic source of subtle bugs.

```go
// Interfaces — satisfied implicitly. No "implements" keyword.
type Stringer interface {
    String() string
}
// Any type with a String() string method IS a Stringer. No declaration needed.
```

This is Go's best idea. **Define interfaces where they are *consumed*, not where they are implemented** — a function should accept the narrowest interface it needs, and the concrete type never has to know the interface exists. Keep them small; `io.Reader` has one method and is the most reused abstraction in the language.

```go
// Generics (Go 1.18+) — useful, but reach for them last
func Map[T, U any](s []T, f func(T) U) []U {
    r := make([]U, len(s))
    for i, v := range s { r[i] = f(v) }
    return r
}
```

### Error handling

Errors are ordinary values. There are no exceptions.

```go
if err != nil {
    return fmt.Errorf("loading config: %w", err)   // %w WRAPS: preserves the chain
}
```

`%w` (Go 1.13+) is the one to use — it lets callers inspect the cause:

```go
var pathErr *os.PathError
if errors.As(err, &pathErr) { /* handle */ }   // is it this TYPE, anywhere in the chain?
if errors.Is(err, os.ErrNotExist) { /* handle */ }  // is it this VALUE?

// Sentinel errors, for conditions callers branch on
var ErrNotFound = errors.New("not found")
```

**Conventions worth internalising:**
- Error strings are lowercase and unpunctuated — they get wrapped into larger sentences.
- Add context as the error travels up. `"not found"` is useless; `"loading user 42: querying db: not found"` is a bug report.
- Handle an error **once**. Logging it *and* returning it means it appears twice in your logs from different layers.
- **`panic` is for programmer error only** — an impossible state, a nil that cannot be nil. A library must not panic on bad input; return an error. `getScrapedData` in the sibling scraper repo panics on any non-200 response, which lets a remote server crash your handler.
- `recover()` exists but is for the top of a request handler or goroutine, not routine control flow.

```go
// defer runs when the function returns — use it for cleanup, immediately after acquiring
f, err := os.Open("file")
if err != nil { return err }
defer f.Close()
```

**Do not `defer` inside a loop** unless you mean it — deferred calls stack up until the *function* returns, not the iteration. Wrap the body in its own function if you need per-iteration cleanup.

### Concurrency

```go
go doSomething()          // start a goroutine — that's it

ch := make(chan int)      // unbuffered: send blocks until a receiver is ready
buf := make(chan int, 10) // buffered: send blocks only when full
ch <- 42                  // send
v := <-ch                 // receive
v, ok := <-ch             // ok is false once the channel is closed and drained
close(ch)                 // only the SENDER closes, and only once
```

```go
// WaitGroup — wait for N goroutines
var wg sync.WaitGroup
for _, u := range urls {
    wg.Add(1)
    go func(u string) {       // Go 1.22+ makes per-iteration loop vars safe,
        defer wg.Done()       // but passing explicitly still reads clearer
        fetch(u)
    }(u)
}
wg.Wait()

// Mutex — when a channel is the wrong shape
var mu sync.Mutex
mu.Lock()
shared++
mu.Unlock()

// select — wait on several channels, with a timeout
select {
case v := <-ch:
    use(v)
case <-time.After(time.Second):
    return errors.New("timed out")
case <-ctx.Done():
    return ctx.Err()
}
```

**`context.Context` is how you cancel work.** Pass it as the first parameter, named `ctx`, to anything that does I/O. It carries deadlines, cancellation, and request-scoped values.

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()                 // ALWAYS defer cancel, or you leak the timer
req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
```

> The weather service calls `http.Get` with no context, so a hung OpenWeatherMap connection ties up that request indefinitely. `http.NewRequestWithContext` plus a client timeout is the fix.

**The rules that keep you out of trouble:**
- *"Don't communicate by sharing memory; share memory by communicating."* Prefer passing data over a channel to guarding it with a mutex.
- **Always know how a goroutine ends.** A goroutine blocked forever on a channel is a leak, and Go will not warn you.
- **Run the race detector**: `go test -race ./...` and `go run -race .`. It finds real bugs that testing alone never will. Use it in CI.
- A `nil` channel blocks forever. Sending on a closed channel panics.

### Testing

Tests live beside the code in `*_test.go`, and the framework is the standard library.

```go
// weather_test.go
package main

import "testing"

func TestDivide(t *testing.T) {
    got, err := divide(10, 2)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)   // Fatal stops this test
    }
    if got != 5 {
        t.Errorf("divide(10,2) = %v, want 5", got)  // Error continues
    }
}
```

**Table-driven tests are the house style across the entire Go ecosystem:**

```go
func TestDivide(t *testing.T) {
    tests := []struct {
        name    string
        a, b    float64
        want    float64
        wantErr bool
    }{
        {"simple", 10, 2, 5, false},
        {"by zero", 1, 0, 0, true},
        {"negative", -10, 2, -5, false},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {     // subtests: named, filterable, parallelisable
            got, err := divide(tt.a, tt.b)
            if (err != nil) != tt.wantErr {
                t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
            }
            if got != tt.want {
                t.Errorf("got %v, want %v", got, tt.want)
            }
        })
    }
}
```

**Testing an HTTP handler** — no server required:

```go
func TestWeatherTemp(t *testing.T) {
    gin.SetMode(gin.TestMode)
    router := gin.New()
    router.GET("/weather_temp", handler)

    w := httptest.NewRecorder()
    req := httptest.NewRequest("GET", "/weather_temp?city=Vancouver", nil)
    router.ServeHTTP(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("status = %d, want 200", w.Code)
    }
}
```

To test code that calls a third-party API, run a `httptest.NewServer` returning a canned response and point the client at its URL. That requires the base URL to be a variable rather than a hard-coded string — which is why "make the endpoint injectable" is a testability improvement, not just tidiness.

```bash
go test ./...                  # everything
go test -v -run TestDivide     # one test, verbose
go test -race ./...            # with the race detector
go test -cover ./...           # coverage percentage
go test -coverprofile=c.out ./... && go tool cover -html=c.out   # coverage in a browser
go test -bench=. ./...         # run benchmarks
```

Useful extras: `t.Parallel()` marks a test safe to run concurrently; `t.Cleanup(fn)` registers teardown; `t.Helper()` in a helper makes failures report the caller's line. `TestMain(m *testing.M)` gives you package-level setup and teardown.

### Best practices

**Formatting and naming**
- Run `gofmt` (or `go fmt ./...`). Non-negotiable — every editor can do it on save.
- `MixedCaps`, never `snake_case`. Acronyms stay uppercase: `userID`, `serveHTTP`, `apiURL`.
- Short names for short scopes — `i`, `r`, `buf` in a five-line function are correct Go, not laziness. Length should scale with the distance between declaration and use.
- Package names are short, lowercase, singular, no underscores. The name is part of every call site: `http.Client` reads well, `utils.HTTPClientUtils` does not.
- **Avoid `util`, `helpers`, `common`, `misc`.** They accrete unrelated code and become a dependency magnet. Name a package for what it provides.

**Structure**
- Accept interfaces, return structs.
- Keep interfaces small — one to three methods. Large interfaces are hard to implement and impossible to fake in tests.
- Put an interface in the consuming package, not the implementing one.
- The zero value of your type should be useful if you can manage it.
- Group related types and their methods in one file; split by responsibility, not by kind. A `models.go` holding every struct in the project is an anti-pattern.

**Correctness**
- Check every error. `_ = doThing()` should be rare and commented.
- Wrap with `%w` and add context.
- Pass `context.Context` as the first argument to anything doing I/O.
- Never store a `Context` in a struct field.
- Set explicit timeouts on HTTP clients — **`http.DefaultClient` has no timeout at all**, so a slow server can hang your process forever:
  ```go
  client := &http.Client{Timeout: 10 * time.Second}
  ```
- `defer resp.Body.Close()` after every successful `http.Get`, or you leak connections.
- Read config from the environment. Never commit secrets; keep a `.env.example` with the *names* and no values.

**Performance — only after measuring**
- Preallocate when you know the size: `make([]T, 0, n)`.
- Use `strings.Builder` to concatenate in a loop, not `+=`.
- Profile with `go test -bench=. -cpuprofile=cpu.out` then `go tool pprof cpu.out`. Guessing at Go performance is usually wrong.

### Common pitfalls

| Trap | What happens | Fix |
|---|---|---|
| Writing to a `nil` map | Panic at runtime | `m := make(map[k]v)` |
| Ignoring `append`'s return | Your data silently vanishes | `s = append(s, v)` |
| `defer` inside a loop | Cleanup piles up until the function ends | Wrap the body in a function |
| Slices sharing an array | Mutating one changes the other | Copy explicitly: `append([]T(nil), s...)` |
| Shadowing with `:=` | Outer `err` never gets the new value | Use `=` when the variable exists |
| A nil pointer in a non-nil interface | `err != nil` is true for a nil `*MyErr` | Return a bare `nil`, not a typed nil |
| Unbuffered channel, no receiver | Deadlock | Buffer it, or receive in another goroutine |
| Depending on map order | Passes locally, fails in CI | Sort the keys |
| Loop variable capture | All goroutines saw the last value | **Fixed in Go 1.22** — per-iteration scope. Below 1.22, pass as a parameter. |

### Gin, in brief

Both services here use [Gin](https://gin-gonic.com/), a fast HTTP router with middleware.

```go
router := gin.Default()          // Logger + Recovery middleware attached
// gin.New() gives you a bare engine with neither

router.GET("/path", func(c *gin.Context) {
    q     := c.Query("name")               // ?name=...  ("" if absent)
    qd    := c.DefaultQuery("name", "fallback")
    p     := c.Param("id")                 // from a /users/:id route
    var body MyStruct
    if err := c.ShouldBindJSON(&body); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return                              // MUST return — Gin does not stop for you
    }
    c.JSON(http.StatusOK, gin.H{"ok": true})
    c.String(http.StatusOK, "plain text")
})

router.Run(":8080")
```

- `gin.H` is shorthand for `map[string]interface{}`.
- **Set `GIN_MODE=release` in production.** Debug mode logs every route and prints a warning banner.
- After writing a response you must `return`. Gin will happily let a handler write twice.
- Group routes with `r.Group("/api/v1")`; add middleware with `router.Use(...)`.

### Building and shipping

```bash
go build -o app .                            # for this machine
GOOS=linux GOARCH=amd64 go build -o app .    # cross-compile: no toolchain needed
GOOS=windows GOARCH=amd64 go build -o app.exe .
GOOS=darwin GOARCH=arm64 go build -o app .   # Apple Silicon

# Smaller binary: strip symbols and DWARF
go build -ldflags="-s -w" -o app .

# Stamp the build with a version at compile time
go build -ldflags="-X main.version=1.2.3" -o app .
```

Cross-compilation is a genuine Go superpower — one command produces a Linux binary from a Mac, with no cross-toolchain.

**A smaller Docker image than the one in this repo.** The `dockerfile` here ships the full `golang:latest` image (~800 MB) because it builds and runs in the same stage. A multi-stage build produces ~15 MB:

```dockerfile
FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /app .

FROM gcr.io/distroless/static-debian12
COPY --from=build /app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
```

`CGO_ENABLED=0` is what makes the binary truly static and able to run on `scratch` or `distroless`.

### Tooling worth installing

```bash
go install golang.org/x/tools/cmd/goimports@latest              # gofmt + fixes imports
go install honnef.co/go/tools/cmd/staticcheck@latest            # the best Go linter
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest  # many linters at once
go install golang.org/x/tools/gopls@latest                      # language server (VS Code)
go install github.com/go-delve/delve/cmd/dlv@latest             # debugger
```

`gopls` + the official Go extension gives you completion, refactoring, and inline errors in VS Code. `staticcheck` catches a large class of real bugs that `go vet` does not.

### Learning further, offline-friendly

- `go doc <package>` — the full standard library, on your machine, no internet.
- **A Tour of Go** — <https://go.dev/tour/> (also runs offline: `go install golang.org/x/website/tour@latest`).
- **Effective Go** — <https://go.dev/doc/effective_go> — still the best statement of Go style.
- **Go Code Review Comments** — <https://go.dev/wiki/CodeReviewComments> — a checklist of what reviewers flag.
- **The Go Programming Language** (Donovan & Kernighan) — the book.

---

## License

MIT — see [LICENSE](LICENSE).

Contributions welcome; pull requests are fine.
