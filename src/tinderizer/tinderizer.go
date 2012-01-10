package main

import (
    "bookmarklet"
    "cache"
    "encoding/json"
    "env"
    "extractor"
    "fmt"
    "github.com/darkhelmet/web.go"
    "job"
    "os"
    "regexp"
    "render"
    "runtime"
)

const Limit = 10
const TTL = 5 * 60 // 5 minutes
var done *regexp.Regexp
var canonicalHost string
var port string

func init() {
    port = env.GetDefault("PORT", "8080")
    canonicalHost = env.GetDefault("CANONICAL_HOST", "")
}

type JSON map[string]interface{}

func startJson(ctx *web.Context) {
    ctx.SetHeader("Access-Control-Allow-Origin", "*", true)
    ctx.SetHeader("Content-Type", "application/json; charset=utf-8", true)
    ctx.StartResponse(200)
}

func renderJson(ctx *web.Context, data JSON) {
    raw, _ := json.Marshal(data)
    ctx.Write(raw)
}

func handleRedirect(ctx *web.Context, f func() string) {
    if canonicalHost != "" && ctx.Host != canonicalHost {
        url := ctx.URL
        url.Host = canonicalHost
        url.Scheme = "http"
        ctx.Redirect(301, url.String())
    } else {
        ctx.StartResponse(200)
        ctx.WriteString(f())
    }
}

func pwd() string {
    cwd, err := os.Getwd()
    if err != nil {
        panic("wat")
    }
    return cwd
}

func main() {
    done = regexp.MustCompile("(?i:done|failed|limited|invalid|error|sorry)")
    web.Config.StaticDir = pwd() + "/static"
    web.Get("/ajax/submit.json", func(ctx *web.Context) {
        startJson(ctx)
        j := job.New(ctx.Params["email"], ctx.Params["url"])
        if j.IsValid() {
            j.Progress("Working...")
            extractor.Extract(j)
            renderJson(ctx, JSON{
                "message": "Submitted! Hang tight...",
                "id":      j.KeyString(),
            })
        } else {
            renderJson(ctx, JSON{
                "message": j.ErrorMessage,
            })
        }
    })

    web.Get("/ajax/status/(.*).json", func(ctx *web.Context, id string) {
        startJson(ctx)

        var message string
        isDone := true

        if v, err := cache.Get(id); err == nil {
            message = v
            isDone = done.MatchString(message)
        } else {
            message = "No job with that ID found."
        }

        renderJson(ctx, JSON{
            "message": message,
            "done":    isDone,
        })
    })

    web.Get("/static/bookmarklet.js", func(ctx *web.Context) {
        ctx.SetHeader("Content-Type", "application/javascript; charset=utf-8", true)
        ctx.StartResponse(200)
        ctx.Write(bookmarklet.Javascript())
    })

    web.Get("/", func(ctx *web.Context) {
        handleRedirect(ctx, func() string {
            return render.Page("index", ctx)
        })
    })

    web.Get("/kindle-email", func(ctx *web.Context) {
        handleRedirect(ctx, func() string {
            return render.Chunk("kindle_email")
        })
    })

    web.Get("/(firefox|safari|chrome|ie|ios)", func(ctx *web.Context, page string) {
        handleRedirect(ctx, func() string {
            return render.Chunk(page)
        })
    })

    web.Get("/(faq|bugs|contact)", func(ctx *web.Context, page string) {
        handleRedirect(ctx, func() string {
            return render.Page(page, ctx)
        })
    })

    web.Get("/debug.json", func(ctx *web.Context) {
        startJson(ctx)
        runtime.UpdateMemStats()
        renderJson(ctx, JSON{
            "version":    runtime.Version(),
            "goroutines": runtime.Goroutines(),
            "GOMAXPROCS": runtime.GOMAXPROCS(0),
            "GOROOT":     runtime.GOROOT(),
            "GOARCH":     runtime.GOARCH,
            "GOOS":       runtime.GOOS,
            "cgocalls":   runtime.Cgocalls(),
            "memstats":   runtime.MemStats,
        })
    })

    web.Run(fmt.Sprintf("0.0.0.0:%s", port))
}
