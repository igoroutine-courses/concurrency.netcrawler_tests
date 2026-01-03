package crawler

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const crawlPath = "/crawl"

const (
	serverUpTTL   = time.Second
	serverDownTTL = time.Second * 15
	serverUpRetry = time.Millisecond * 20
)

const (
	contentTypeJson = "application/json"
)

func startCrawlerServer(ctx context.Context, t *testing.T) (baseURL *url.URL, stopWait func()) {
	t.Helper()

	c := New()
	port := findFreePort(t)
	errCh := make(chan error, 1)

	go func() {
		defer close(errCh)
		errCh <- c.ListenAndServe(ctx, port)
	}()

	fullAddr, err := url.Parse("http://127.0.0.1" + port)
	require.NoError(t, err)

	up := waitHTTPUp(t, fullAddr, serverUpTTL)
	require.Truef(t, up, "failed to start server: %sf", fullAddr.String())

	stopWait = func() {
		select {
		case err := <-errCh:
			require.NoError(t, err) // hint: try to use Shutdown for graceful stop
		case <-time.After(serverDownTTL):
			t.Fatalf("server did not stop in time")
		}
	}

	return fullAddr, stopWait
}

func waitHTTPUp(t *testing.T, baseURL *url.URL, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 200 * time.Millisecond}
	p := constructCrawlPath(t, baseURL)

	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, p.String(), nil)
		_, err := client.Do(req)

		if err == nil {
			return true
		}

		time.Sleep(serverUpRetry)
	}

	return false
}

func constructCrawlPath(t *testing.T, baseURL *url.URL) *url.URL {
	t.Helper()
	return baseURL.JoinPath(crawlPath)
}

func client() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   runtime.GOMAXPROCS(-1),
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second * 3,
			ResponseHeaderTimeout: time.Minute,
		},
	}
}

func makeURLs(t *testing.T, baseURL string, n int) []string {
	t.Helper()

	urls := make([]string, n)
	for i := 0; i < n; i++ {
		urls[i] = fmt.Sprintf("%s/item-%d", baseURL, i)
	}
	return urls
}

func findFreePort(t *testing.T) string {
	t.Helper()

	for {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		port := ln.Addr().(*net.TCPAddr).Port
		if err = ln.Close(); err != nil {
			continue
		}

		return ":" + strconv.Itoa(port)
	}
}

func newConnCountingServer(t *testing.T) (srv *httptest.Server, conns *atomic.Int64) {
	t.Helper()
	conns = &atomic.Int64{}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 128<<10))
	})

	srv = httptest.NewUnstartedServer(h)

	srv.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}

	srv.Start()
	return srv, conns
}
