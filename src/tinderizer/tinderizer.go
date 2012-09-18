package main

import (
    "bookmarklet"
    "bytes"
    "cache"
    "cleaner"
    "emailer"
    "encoding/json"
    "extractor"
    "fmt"
    "github.com/darkhelmet/env"
    "github.com/garyburd/twister/expvar"
    "github.com/garyburd/twister/pprof"
    "github.com/garyburd/twister/server"
    "github.com/garyburd/twister/web"
    "html/template"
    "io"
    J "job"
    "kindlegen"
    "log"
    "net"
    "os"
    "regexp"
    "stat"
)

type JSON map[string]interface{}

var (
    doneRegex     = regexp.MustCompile("(?i:done|failed|limited|invalid|error|sorry)")
    port          = env.IntDefault("PORT", 8080)
    canonicalHost = env.StringDefaultF("CANONICAL_HOST", func() string { return fmt.Sprintf("localhost:%d", port) })
    logger        = log.New(os.Stdout, "[server] ", env.IntDefault("LOG_FLAGS", log.LstdFlags|log.Lmicroseconds))
    templates     = template.Must(template.ParseGlob("views/*.tmpl"))
    newJobs       chan J.Job
)

func init() {
    stat.Count(stat.RuntimeBoot, 1)
    newJobs = RunApp()
}

func RunApp() chan J.Job {
    input := make(chan J.Job, 10)
    conversion := make(chan J.Job, 10)
    emailing := make(chan J.Job, 10)
    cleaning := make(chan J.Job, 10)

    go extractor.New(input, conversion, cleaning).Run()
    go kindlegen.New(conversion, emailing, cleaning).Run()
    go emailer.New(emailing, cleaning, cleaning).Run()
    go cleaner.New(cleaning).Run()

    return input
}

func renderPage(w io.Writer, page, host string) error {
    var buffer bytes.Buffer
    if err := templates.ExecuteTemplate(&buffer, page, nil); err != nil {
        return err
    }
    return templates.ExecuteTemplate(w, "layout.tmpl", JSON{
        "host":  host,
        "yield": template.HTML(buffer.String()),
    })
}

func handleBookmarklet(req *web.Request) {
    w := req.Respond(web.StatusOK, web.HeaderContentType, "application/javascript; charset=utf-8")
    w.Write(bookmarklet.Javascript())
}

func pageHandler(req *web.Request) {
    w := req.Respond(web.StatusOK, web.HeaderContentType, "text/html; charset=utf-8")
    tmpl := fmt.Sprintf("%s.tmpl", req.URLParam["page"])
    if err := renderPage(w, tmpl, canonicalHost); err != nil {
        logger.Printf("failed rendering page: %s", err)
    }
}

func chunkHandler(req *web.Request) {
    w := req.Respond(web.StatusOK, web.HeaderContentType, "text/html; charset=utf-8")
    tmpl := fmt.Sprintf("%s.tmpl", req.URLParam["chunk"])
    if err := templates.ExecuteTemplate(w, tmpl, nil); err != nil {
        logger.Printf("failed rendering chunk: %s", err)
    }
}

func homeHandler(req *web.Request) {
    w := req.Respond(web.StatusOK, web.HeaderContentType, "text/html; charset=utf-8")
    if err := renderPage(w, "index.tmpl", canonicalHost); err != nil {
        logger.Printf("failed rendering index: %s", err)
    }
}

type Submission struct {
    Url     string `json:"url"`
    Email   string `json:"email"`
    Content string `json:"content"`
}

func submitHandler(req *web.Request) {
    decoder := json.NewDecoder(req.Body)
    var submission Submission
    err := decoder.Decode(&submission)
    if err != nil {
        logger.Printf("failed decoding submission: %s", err)
    }
    logger.Printf("submission of %#v to %#v", submission.Url, submission.Email)

    w := req.Respond(web.StatusOK,
        web.HeaderContentType, "application/json; charset=utf-8",
        "Access-Control-Allow-Origin", "*")

    encoder := json.NewEncoder(w)
    job := J.New(submission.Email, submission.Url, submission.Content)
    if err := job.Validate(); err == nil {
        stat.Count(stat.SubmitSuccess, 1)
        job.Progress("Working...")
        newJobs <- *job
        encoder.Encode(JSON{
            "message": "Submitted! Hang tight...",
            "id":      job.Key.String(),
        })
    } else {
        stat.Count(stat.SubmitBlacklist, 1)
        encoder.Encode(JSON{
            "message": err.Error(),
        })
    }
    stat.Count("submitHandler", 1)
    stat.Debug()
}

func oldSubmitHandler(req *web.Request) {
    w := req.Respond(web.StatusOK,
        web.HeaderContentType, "application/json; charset=utf-8",
        "Access-Control-Allow-Origin", "*")
    encoder := json.NewEncoder(w)
    job := J.New(req.Param.Get("email"), req.Param.Get("url"), "")
    if err := job.Validate(); err == nil {
        stat.Count(stat.SubmitSuccess, 1)
        job.Progress("Working...")
        newJobs <- *job
        encoder.Encode(JSON{
            "message": "Submitted! Hang tight...",
            "id":      job.Key.String(),
        })
    } else {
        stat.Count(stat.SubmitBlacklist, 1)
        encoder.Encode(JSON{
            "message": err.Error(),
        })
    }
    stat.Count("oldSubmitHandler", 1)
    stat.Debug()
}

func statusHandler(req *web.Request) {
    w := req.Respond(web.StatusOK,
        web.HeaderContentType, "application/json; charset=utf-8",
        "Access-Control-Allow-Origin", "*")
    message := "No job with that ID found."
    done := true
    if v, err := cache.Get(req.URLParam["id"]); err == nil {
        message = v
        done = doneRegex.MatchString(message)
    }
    encoder := json.NewEncoder(w)
    encoder.Encode(JSON{
        "message": message,
        "done":    done,
    })
}

func redirectHandler(req *web.Request) {
    stat.Count(stat.HttpRedirect, 1)
    url := req.URL
    url.Host = canonicalHost
    url.Scheme = "http"
    req.Respond(web.StatusMovedPermanently, web.HeaderLocation, url.String())
}

func ShortLogger(lr *server.LogRecord) {
    if lr.Error != nil {
        logger.Printf("%d %s %s %s\n", lr.Status, lr.Request.Method, lr.Request.URL, lr.Error)
    } else {
        logger.Printf("%d %s %s\n", lr.Status, lr.Request.Method, lr.Request.URL)
    }
}

func main() {
    submitRoute := "/ajax/submit.json"
    statusRoute := "/ajax/status/<id:[^.]+>.json"
    router := web.NewRouter().
        Register("/", "GET", homeHandler).
        Register("/static/bookmarklet.js", "GET", handleBookmarklet).
        Register("/<page:(faq|bugs|contact)>", "GET", pageHandler).
        Register("/<chunk:(firefox|safari|chrome|ie|ios|kindle-email)>", "GET", chunkHandler).
        Register(submitRoute, "POST", submitHandler, "GET", oldSubmitHandler).
        Register(statusRoute, "GET", statusHandler).
        Register("/debug.json", "GET", expvar.ServeWeb).
        Register("/debug/pprof/<:.*>", "*", pprof.ServeWeb).
        Register("/<path:.*>", "GET", web.DirectoryHandler("public", nil))

    redirector := web.NewRouter().
        // These routes get matched in both places so they work everywhere.
        Register(submitRoute, "POST", submitHandler, "GET", oldSubmitHandler).
        Register(statusRoute, "GET", statusHandler).
        Register("/<splat:>", "GET", redirectHandler)

    hostRouter := web.NewHostRouter(redirector).
        Register(canonicalHost, router)

    listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
    if err != nil {
        logger.Fatalf("failed to listen: %s", err)
    }
    defer listener.Close()
    server := &server.Server{
        Listener: listener,
        Handler:  hostRouter,
        Logger:   server.LoggerFunc(ShortLogger),
    }
    logger.Printf("Tinderizer is starting on 0.0.0.0:%d", port)
    err = server.Serve()
    if err != nil {
        logger.Fatalf("failed to serve: %s", err)
    }
}
