package main

// @review you haven't been using `goimports` or `go fmt`, tsk tsk.
// I did run it with go install --race do spot any obvious race conditions, but you haven't done any obvious ones, so good on ya

import (
    "fmt"
    "os"
    "os/signal"
    "syscall"
    "io"
    "io/ioutil"
    "log"
    "net/http"
    // @review: use a tool like https://godoc.org/golang.org/x/tools/cmd/goimports, hook it into your IDE on save and it will format and fix your imports automatically
    // "math/rand"
    "time"

    "github.com/gorilla/websocket"
    "github.com/tcnksm/go-httpstat"
)


type Worker func(id int)

// @review if you want to be a little bit safer, use uint since I don't think you want -4 concurrency possible? :P
type RunningState struct {
    NumRequests int
    Concurrency int
    Running bool
}

func main() {
    fmt.Println("Concurrency test")

    state := RunningState{}
    // @review no need to initialised a non pointer value, the default int is 0
    state.NumRequests = 0
    // @review no need to initialised a non pointer value, the default bool is false
    state.Running = false

    // @review are you sure you want these buffered? i.e. non blocking?
    startSignal := make(chan bool, 1)
    stopSignal := make(chan bool, 1)

    // Display a web dashboard
    go func() {
        http.Handle("/", http.FileServer(http.Dir("./public")))
        http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
           wsHandler(w, r, &state, startSignal, stopSignal)
        })

        // @review in a proper production scenario I would have insisted on a proper http.Server.Close() on application shutdown
        if err := http.ListenAndServe(":8080", nil); err != nil {
          panic(err)
        }
    }()
    fmt.Println("Dashboard: http://localhost:8080")

    var gracefulStop = make(chan os.Signal)
    signal.Notify(gracefulStop, syscall.SIGTERM)
    signal.Notify(gracefulStop, syscall.SIGINT)
    go func() {
        <-gracefulStop

        fmt.Println("")
        fmt.Println("Stopping...")
        // @review I don't see any graceful stopping / waiting here? need to implement sending stop signal and waiting for that?
        fmt.Println("Waiting for active workers to complete...")
        fmt.Println("")

        os.Exit(0)
    }()

    // @review my code style would have made me do something like this https://gist.github.com/stojg/0d161b32089b4ceb3bbd3c1999b8c868
    // so that I could write tests for concurrency and the single worker easily and extracted it out of main
    start := func() {
        <-startSignal

        // Create our worker with a buffer which matches our target
        // concurrency.
        workers := make(chan bool, state.Concurrency)


        // Do the actual requests with the required concurrency
        // @review, looks like id is unused?
        withConcurrency(workers, func(id int) {
            // Create our request
            req, err := http.NewRequest("GET", "http://localhost/", nil)

            if err != nil {
                log.Print(err)
                return
            }

            // @review I assume you will do something with httpstat laters
            // Wrap our request with httpstat
            var result httpstat.Result
            ctx := httpstat.WithHTTPStat(req.Context(), &result)
            req = req.WithContext(ctx)

            // Make the request
            client := http.DefaultClient
            res, err := client.Do(req)
            if err != nil {
                log.Print(err)
                return
            }

            // No wait for the response to complete and check for errors
            if _, err := io.Copy(ioutil.Discard, res.Body); err != nil {
                log.Print(err)
                // @review maybe you should close the Body() here as well?
                return
            }
            res.Body.Close()
            result.End(time.Now())

            // @review this might, or might not be race safe when multiple workers are adding to the num request, I would
            // investigate or implement a mutex on the RunningState struct
            state.NumRequests++

            // fmt.Printf("%d: %+v\n", result)
            fmt.Println(res.StatusCode)

            // fmt.Printf("%+v\n", result)
        }, stopSignal)
    }

    // This will block until the start signal is sent
    for {
        start()
    }
}


// @review I would have split this section out into a different file, ie concurrency.go
////////////////////////////////////////////////////////////////////////
//
// Concurrency Handling
//
////////////////////////////////////////////////////////////////////////
// @review i would just have initialied the `workers` chan in hear and pass in the concurrency number I wanted instead of
// having to define workers outside of this scope
func withConcurrency(workers chan bool, work func(id int), stopSignal chan bool) {
    // @trivia if I was using millions of workers, I would have used empty structs as the chan type, since they weirdly consumes zero bytes in memory https://dave.cheney.net/2014/03/25/the-empty-struct
    // Fill up our channel buffer with some workers, ready
    // for action
    for j := 0; j < cap(workers); j++ {
        workers <- true
    }

    for i := 0; ; i++ {
        select  {
            case <-workers:
                go func(id int, w chan bool) {
                    work(id); // @review unnecessary `;` go fmt would have removed it

                    w <- true
                }(i, workers)
            case <-stopSignal:
                // @review don't you want to wait for the last workers to finish? ie fill up the workers chan to cap()
                return
        }
    }
}


// @review I would have split this section out into a different file, ie websocket.go or something like that
////////////////////////////////////////////////////////////////////////
//
// Websocket Handling
//
////////////////////////////////////////////////////////////////////////
type Request struct {
    Action string
    Concurrency int
}

type Response struct {
    State RunningState
}

// @review here is a fun fact that me and a friend discovered, more of a trivia.. vodaphone (maybe others) blocks
// ws traffic over http, only works on https for some stupid reason :P
func wsHandler(w http.ResponseWriter, r *http.Request, state *RunningState, startSignal chan bool, stopSignal chan bool) {
    if r.Header.Get("Origin") != "http://" + r.Host {
        http.Error(w, "Origin not allowed", 403)
        return
    }

    // @review, this is marked as Deprecated: Use websocket.Upgrader instead in the api docs
    conn, err := websocket.Upgrade(w, r, w.Header(), 1024, 1024)
    if err != nil {
        http.Error(w, "Could not open websocket connection", http.StatusBadRequest)
        // @review, you probably want to return here?
    }

    go echo(conn, state, startSignal, stopSignal)
}

// @review echo.. copy pasta function name from the docs?
func echo(conn *websocket.Conn, state *RunningState, startSignal chan bool, stopSignal chan bool) {
    var message Request
    response := Response{}

    // @review you might wanna exit this infinite loop in case of the connection going bad. Otherwise you would have a leaking
    // go routinue that would never close.. for example after X amount failed read or writes to the conn.. or other specific errors
    for {
        err := conn.ReadJSON(&message)
        if err != nil {
            fmt.Println("Error reading json: ", err)
        }

        switch message.Action {
            // @review I normally turn these ("start", "stop" etc) into int constants of a type, so I don't have to worry about typing it correctly
            // ie. https://gist.github.com/stojg/706a92fc077405405903a5c40db7d743
            case "start":
                // Start
                fmt.Println("Starting")
                state.Running = true
                state.Concurrency = message.Concurrency
                startSignal <- true
                break
            case "stop":
                // Stop
                fmt.Println("Stopping")
                state.Running = false
                stopSignal <- true
                break
            case "update":
                // Restart
                fmt.Println("Update")
                state.Running = true
                stopSignal <- true
                startSignal <- true
                state.Concurrency = message.Concurrency
                break
            default:
        }

        response.State = *state

        if err := conn.WriteJSON(&response); err != nil {
            fmt.Println(err)
        }
    }
}
