package main

import (
	"context"
	"flag"
	"net"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
	app "gopkg.in/crast/app.v0"

	"github.com/crast/dvr-tools/cmd/seeker/logic"
)

func main() {
	debugMode := flag.Bool("debug", false, "Debug")
	flag.StringVar(&logic.Token, "token", "", "Token")
	flag.StringVar(&logic.PlexServer, "plex", "http://192.168.1.2:32400", "plex server")
	flag.StringVar(&logic.WatchLogDir, "log-dir", "", "watch log dir")
	flag.Parse()
	if *debugMode {
		logrus.SetLevel(logrus.DebugLevel)
	}

	r := logic.GetRouter()

	baseCtx, cancel := context.WithCancel(context.Background())
	app.AddCloser(func() error {
		logrus.Debugf("Running closer")
		cancel()
		return nil
	})

	app.Go(func() {
		logic.GlobalPoller(baseCtx)
	})

	server := &http.Server{
		Handler: r,
	}

	app.AddCloser(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	})

	app.Go(func() error {
		lis, err := net.Listen("tcp", ":8081")
		if err != nil {
			logrus.Fatal(err)
		}
		return server.Serve(lis)

	})

	app.Main()
}
