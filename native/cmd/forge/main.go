// forge is a standard-library-only static server. It owns registration and done
// polling; the Node entrypoint only selects and supervises this process.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"forge/api"
	"forge/transport"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"syscall"
	"time"
)

func retry(attempt int) { time.Sleep(min(2*time.Second, 250*time.Millisecond<<min(attempt, 8))) }
func lifecycle(env string, port int) error {
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		MaxIdleConns: 1, MaxIdleConnsPerHost: 1,
	}}
	payload := []byte(fmt.Sprintf(`{"port":%d}`, port))
	for attempt := 0; ; attempt++ {
		res, e := client.Post(env+"/register", "application/json", bytes.NewReader(payload))
		if e == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode == 200 || res.StatusCode == 409 {
				break
			}
			if res.StatusCode == 400 || res.StatusCode == 405 {
				return fmt.Errorf("register answered HTTP %d", res.StatusCode)
			}
		}
		retry(attempt)
	}
	fmt.Fprintln(os.Stdout, "[entry] registered; waiting for done")
	for attempt := 0; ; attempt++ {
		res, e := client.Get(env + "/done")
		if e == nil {
			var data struct {
				Done bool `json:"done"`
			}
			e = json.NewDecoder(res.Body).Decode(&data)
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode == 200 && e == nil && data.Done {
				return nil
			}
		}
		retry(attempt)
	}
}
func run() error {
	runtime.GOMAXPROCS(2)
	debug.SetGCPercent(500)
	debug.SetMemoryLimit(600 << 20)
	env := strings.TrimRight(os.Getenv("MAKEFUN_ENV_URL"), "/")
	if env == "" {
		return fmt.Errorf("MAKEFUN_ENV_URL is not set: there is no env to register with")
	}
	if path := os.Getenv("FORGE_CPU_PROFILE"); path != "" {
		f, e := os.Create(path)
		if e != nil {
			return e
		}
		if e = pprof.StartCPUProfile(f); e != nil {
			f.Close()
			return e
		}
		defer func() { pprof.StopCPUProfile(); f.Close() }()
	}
	app := api.New(os.Getenv("MAKEFUN_SHOP_TOKEN"))
	serve := transport.ServeOwner
	if os.Getenv("FORGE_TRANSPORT") == "netpoll" {
		serve = transport.ServeNetpoll
	}
	port, closeServer, e := serve(app.Handle)
	if e != nil {
		return e
	}
	defer closeServer()
	fmt.Fprintf(os.Stdout, "[entry] native %s listening on port %d\n", runtime.GOARCH, port)
	if e = lifecycle(env, port); e != nil {
		return e
	}
	if path := os.Getenv("FORGE_HEAP_PROFILE"); path != "" {
		f, e := os.Create(path)
		if e != nil {
			return e
		}
		pprof.WriteHeapProfile(f)
		f.Close()
	}
	var usage syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	fmt.Fprintf(os.Stderr, "[entry] peak RSS %d KiB\n", usage.Maxrss)
	fmt.Fprintln(os.Stdout, "[entry] done")
	return nil
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "[entry]", e)
		os.Exit(1)
	}
}
