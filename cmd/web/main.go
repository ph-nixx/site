package main

import (
	"flag"
	"net/http"

	"github.com/ph-nixx/site/cmd/web/app"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP network address")
	flag.Parse()
	a, err := app.New()
	if err != nil {
		panic(err)
	}
	if err := http.ListenAndServe(*addr, a.Routes()); err != nil {
		a.Logger.Error(err.Error())
	}
}
