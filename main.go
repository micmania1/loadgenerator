package main

import (
    "fmt"
    "os"
    "os/signal"
    "syscall"
    // "math/rand"
    "time"
    "net/http"
    "log"
    "io"
    "io/ioutil"

    "github.com/tcnksm/go-httpstat"
    "github.com/gorilla/websocket"
)

type RunningState struct {
    NumRequests int
    Concurrency int
    Running bool
}

func main() {
    fmt.Println("Concurrency test")

    state := RunningState{}
    state.NumRequests = 0
    state.Running = false

    startSignal := make(chan bool, 1)
    stopSignal := make(chan bool, 1)

    // Display a web dashboard
    go func() {
        http.Handle("/", http.FileServer(http.Dir("./public")))
        http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
           wsHandler(w, r, &state, startSignal, stopSignal)
        })

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
        fmt.Println("Waiting for active workers to complete...")
        fmt.Println("")

        os.Exit(0)
    }()

    start := func() {
        <-startSignal

        // Create our worker with a buffer which matches our target
        // concurrency.
        workers := make(chan bool, state.Concurrency)


        // Do the actual requests with the required concurrency
        withConcurrency(workers, func(id int) {
            // Create our request
            req, err := http.NewRequest("GET", "http://localhost/", nil)

            if err != nil {
                log.Print(err)
                return
            }

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
                return
            }
            res.Body.Close()
            result.End(time.Now())

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


////////////////////////////////////////////////////////////////////////
//
// Concurrency Handling
//
////////////////////////////////////////////////////////////////////////

func withConcurrency(workers chan bool, work func(id int), stopSignal chan bool) {
    // Fill up our channel buffer with some workers, ready
    // for action
    for j := 0; j < cap(workers); j++ {
        workers <- true
    }

    for i := 0; ; i++ {
        select  {
            case <-workers:
                go func(id int, w chan bool) {
                    work(id);

                    w <- true
                }(i, workers)
            case <-stopSignal:
                return
        }
    }
}


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

func wsHandler(w http.ResponseWriter, r *http.Request, state *RunningState, startSignal chan bool, stopSignal chan bool) {
    if r.Header.Get("Origin") != "http://" + r.Host {
        http.Error(w, "Origin not allowed", 403)
        return
    }

    conn, err := websocket.Upgrade(w, r, w.Header(), 1024, 1024)
    if err != nil {
        http.Error(w, "Could not open websocket connection", http.StatusBadRequest)
    }

    go echo(conn, state, startSignal, stopSignal)
}

func echo(conn *websocket.Conn, state *RunningState, startSignal chan bool, stopSignal chan bool) {
    var message Request
    response := Response{}

    for {
        err := conn.ReadJSON(&message)
        if err != nil {
            fmt.Println("Error reading json: ", err)
        }

        switch message.Action {
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
