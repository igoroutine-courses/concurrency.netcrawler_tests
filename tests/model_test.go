//go:build model_test

package crawler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCrawlMethodNotAllowed(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	resp, err := http.Get(p)

	require.NoError(t, err)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	require.EqualValues(t, http.MethodPost, resp.Header.Get("Allow"))
}

func TestCrawlHandlerBadJSON(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()
	req, err := http.NewRequest(http.MethodPost, p, bytes.NewBufferString("{not-json"))
	require.NoError(t, err)

	resp, err := c.Do(req)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestCrawlHandlerUnknownField(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	const body = `{"urls":["http://example.com"],"workers":1,"timeout_ms":1000,"extra":123}`
	req, err := http.NewRequest(http.MethodPost, p, bytes.NewBufferString(body))
	require.NoError(t, err)

	resp, err := c.Do(req)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestCrawlHandlerZeroWorkers(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	const body = `{"urls":["http://example.com"],"workers": 0,"timeout_ms":1000,"extra":123}`
	req, err := http.NewRequest(http.MethodPost, p, bytes.NewBufferString(body))
	require.NoError(t, err)

	resp, err := c.Do(req)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestCrawlHandlerInfWorkers(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	// hint: expected appropriate limit
	body := fmt.Sprintf(`{"urls":["http://example.com"],"workers":%d,"timeout_ms":1000}`, math.MaxInt)
	req, err := http.NewRequest(http.MethodPost, p, bytes.NewBufferString(body))
	require.NoError(t, err)

	resp, err := c.Do(req)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestCrawlHandlerEmptyURLs(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	const body = `{"urls":[],"workers":1,"timeout_ms":1000}`
	req, err := http.NewRequest(http.MethodPost, p, bytes.NewBufferString(body))
	require.NoError(t, err)

	resp, err := c.Do(req)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []CrawlResponse
	err = json.NewDecoder(resp.Body).Decode(&got)

	require.NoError(t, err)
	require.Zero(t, len(got))
}

func TestInvalidURLs(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	urls := []string{
		"http://example.com:abc", // bad port
		"http://[::1",            // broken IPv6
		"http://%41",             // invalid URL escape
		"://example.com",         // missing scheme
	}

	reqBody, _ := json.Marshal(CrawlRequest{
		URLs:      urls,
		Workers:   len(urls),
		TimeoutMS: 3000,
	})

	req, err := http.NewRequest(http.MethodPost, p, bytes.NewReader(reqBody))
	require.NoError(t, err)

	resp, err := c.Do(req)
	require.NoError(t, err)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	desc := string(data)

	require.Contains(t, desc, "invalid port")
	require.Contains(t, desc, "missing ']' in host")
	require.Contains(t, desc, "invalid URL escape")
	require.Contains(t, desc, "missing protocol scheme")

	// hint: see url.Parse
}

func TestCrawlHandlerOrderPreserved(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	var (
		hits atomic.Int64
		wg   sync.WaitGroup
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer wg.Done()

		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	const n = 27
	wg.Add(n)
	urls := makeURLs(t, srv.URL, n)

	reqBody, _ := json.Marshal(CrawlRequest{
		URLs:      urls,
		Workers:   n,
		TimeoutMS: 3000,
	})

	req, err := http.NewRequest(http.MethodPost, p, bytes.NewReader(reqBody))
	require.NoError(t, err)

	resp, err := c.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	var got []CrawlResponse
	err = json.NewDecoder(resp.Body).Decode(&got)
	require.NoError(t, err)

	require.Equal(t, len(urls), len(got))

	for i := range urls {
		require.Equal(t, urls[i], got[i].URL)
		require.Empty(t, got[i].Error)
		require.Equal(t, http.StatusNoContent, got[i].StatusCode)
	}

	wg.Wait()
	require.Equal(t, int64(len(urls)), hits.Load())
}

func TestCrawlHandlerMediumTimeout(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	var (
		hits atomic.Int64
		wg   sync.WaitGroup
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer wg.Done()
		time.Sleep(time.Second)

		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	const n = 27
	wg.Add(n)
	urls := makeURLs(t, srv.URL, n)

	reqBody, _ := json.Marshal(CrawlRequest{
		URLs:      urls,
		Workers:   n,
		TimeoutMS: 30_000,
	})

	req, err := http.NewRequest(http.MethodPost, p, bytes.NewReader(reqBody))
	require.NoError(t, err)

	resp, err := c.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	var got []CrawlResponse
	err = json.NewDecoder(resp.Body).Decode(&got)
	require.NoError(t, err)

	require.Equal(t, len(urls), len(got))

	for i := range urls {
		require.Equal(t, urls[i], got[i].URL)
		require.Empty(t, got[i].Error)
		require.Equal(t, http.StatusNoContent, got[i].StatusCode)
	}

	wg.Wait()
	require.Equal(t, int64(len(urls)), hits.Load())
}

func TestCrawlHandlerTimeout(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	var (
		hits atomic.Int64
		wg   sync.WaitGroup
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer wg.Done()

		time.Sleep(3 * time.Second)

		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	const n = 27
	urls := makeURLs(t, srv.URL, n)
	wg.Add(n)

	reqBody, _ := json.Marshal(CrawlRequest{
		URLs:      urls,
		Workers:   n,
		TimeoutMS: 500,
	})

	req, err := http.NewRequest(http.MethodPost, p, bytes.NewReader(reqBody))
	require.NoError(t, err)

	resp, err := c.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	var got []CrawlResponse
	err = json.NewDecoder(resp.Body).Decode(&got)
	require.NoError(t, err)

	require.Equal(t, len(urls), len(got))

	for i := range urls {
		require.Equal(t, urls[i], got[i].URL)
		require.Contains(t, got[i].Error, "timeout exceeded") // need to handle error
		require.Zero(t, got[i].StatusCode)
	}

	wg.Wait()
}

func TestCrawlDedupSameURLConcurrent(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	var (
		hits atomic.Int64
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	const n = 100
	urls := make([]string, 0, n)
	for range n {
		urls = append(urls, srv.URL)
	}

	reqBody, err := json.Marshal(CrawlRequest{
		URLs:      urls,
		Workers:   32,
		TimeoutMS: 2000,
	})
	require.NoError(t, err)

	resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody))
	require.NoError(t, err)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []CrawlResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, n)
	for i := range got {
		require.Empty(t, got[i].Error)
		require.Equal(t, http.StatusOK, got[i].StatusCode)
		require.Equal(t, srv.URL, got[i].URL)
	}

	require.EqualValues(t, 1, hits.Load(),
		"expected exactly one upstream request for duplicate URLs")
}

func TestCrawlDedupSameURLConcurrentWithNormalization(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	var (
		hits atomic.Int64
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	// hint: see path.Clean and url.Parse
	urls := []string{
		srv.URL + "/a/..",
		srv.URL + "/a/b/../..",
		srv.URL + "/a/b/c/../../..",
	}

	reqBody, err := json.Marshal(CrawlRequest{
		URLs:      urls,
		Workers:   32,
		TimeoutMS: 2000,
	})
	require.NoError(t, err)

	resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody))
	require.NoError(t, err)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []CrawlResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, len(urls))
	for i := range got {
		require.Empty(t, got[i].Error)
		require.Equal(t, http.StatusOK, got[i].StatusCode)
		require.Equal(t, urls[i], got[i].URL)
	}

	require.EqualValues(t, 1, hits.Load(),
		"expected exactly one upstream request for duplicate URLs")
}

func TestCrawlDedupSameURLConcurrentWithExtraNormalization(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	var (
		hits atomic.Int64
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	urls := []string{
		srv.URL + "/a%2Fb?1=hello&2=ohhh",
		srv.URL + "/a/b?2=ohhh&1=hello",
	}

	reqBody, err := json.Marshal(CrawlRequest{
		URLs:      urls,
		Workers:   32,
		TimeoutMS: 2000,
	})
	require.NoError(t, err)

	resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody))
	require.NoError(t, err)

	t.Cleanup(func() {
		resp.Body.Close()
	})

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []CrawlResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, len(urls))
	for i := range got {
		require.Empty(t, got[i].Error)
		require.Equal(t, http.StatusOK, got[i].StatusCode)
		require.Equal(t, urls[i], got[i].URL)
	}

	require.EqualValues(t, 1, hits.Load(),
		"expected exactly one upstream request for duplicate URLs")
}

func TestFetchWithCacheTTL(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	var (
		hits atomic.Int64
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	urls := makeURLs(t, srv.URL, 1)

	reqBody, err := json.Marshal(CrawlRequest{
		URLs:      urls,
		Workers:   32,
		TimeoutMS: 2000,
	})
	require.NoError(t, err)

	const k = 10

	// const cacheTTL = time.Second
	for range k {
		time.Sleep(time.Millisecond * 50)

		resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody))
		require.NoError(t, err)

		func() {
			defer resp.Body.Close()

			var got []CrawlResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
			require.Len(t, got, 1)
			require.Equal(t, urls[0], got[0].URL)
		}()
	}

	require.EqualValues(t, 1, hits.Load())

	time.Sleep(cacheTTL + time.Millisecond*100)

	resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody))
	require.NoError(t, err)

	func() {
		defer resp.Body.Close()

		var got []CrawlResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got, 1)
		require.Equal(t, urls[0], got[0].URL)
	}()

	require.EqualValues(t, 2, hits.Load())
}

func TestFetchWithCacheTTLWithNormalization(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	var (
		hits atomic.Int64
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	// hint: see path.Clean and url.Parse

	urls1 := []string{
		srv.URL + "/a/..",
	}

	urls2 := []string{
		srv.URL + "/a/b/../..",
	}

	urls3 := []string{
		srv.URL + "/a/b/c/../../..",
	}

	reqBody1, err := json.Marshal(CrawlRequest{
		URLs:      urls1,
		Workers:   32,
		TimeoutMS: 2000,
	})
	require.NoError(t, err)

	const k = 10

	reqBody2, err := json.Marshal(CrawlRequest{
		URLs:      urls2,
		Workers:   32,
		TimeoutMS: 2000,
	})
	require.NoError(t, err)

	reqBody3, err := json.Marshal(CrawlRequest{
		URLs:      urls3,
		Workers:   32,
		TimeoutMS: 2000,
	})
	require.NoError(t, err)

	// const cacheTTL = time.Second
	for range k / 3 {
		time.Sleep(time.Millisecond * 50)

		resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody1))
		require.NoError(t, err)

		func() {
			defer resp.Body.Close()

			var got []CrawlResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
			require.Len(t, got, 1)
			require.Equal(t, urls1[0], got[0].URL)
		}()
	}

	for range k / 3 {
		time.Sleep(time.Millisecond * 50)

		resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody2))
		require.NoError(t, err)

		func() {
			defer resp.Body.Close()

			var got []CrawlResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
			require.Len(t, got, 1)
			require.Equal(t, urls2[0], got[0].URL)
		}()
	}

	for range k / 3 {
		time.Sleep(time.Millisecond * 50)

		resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody3))
		require.NoError(t, err)

		func() {
			defer resp.Body.Close()

			var got []CrawlResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
			require.Len(t, got, 1)
			require.Equal(t, urls3[0], got[0].URL)
		}()
	}

	require.EqualValues(t, 1, hits.Load())
	time.Sleep(cacheTTL + time.Millisecond*100)

	resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody1))
	require.NoError(t, err)

	func() {
		defer resp.Body.Close()

		var got []CrawlResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got, 1)
		require.Equal(t, urls1[0], got[0].URL)
	}()

	require.EqualValues(t, 2, hits.Load())
}

func TestFetchWithCacheTTLWithExtraNormalization(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	var (
		hits atomic.Int64
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	// hint: see path.Clean and url.Parse

	urls1 := []string{
		srv.URL + "/a%2Fb?1=hello&2=ohhh",
	}

	urls2 := []string{
		srv.URL + "/a/b?2=ohhh&1=hello",
	}

	urls3 := []string{
		srv.URL + ":80/a/b?2=ohhh&1=hello",
	}

	reqBody1, err := json.Marshal(CrawlRequest{
		URLs:      urls1,
		Workers:   32,
		TimeoutMS: 2000,
	})
	require.NoError(t, err)

	const k = 10

	reqBody2, err := json.Marshal(CrawlRequest{
		URLs:      urls2,
		Workers:   32,
		TimeoutMS: 2000,
	})
	require.NoError(t, err)

	reqBody3, err := json.Marshal(CrawlRequest{
		URLs:      urls3,
		Workers:   32,
		TimeoutMS: 2000,
	})
	require.NoError(t, err)

	// const cacheTTL = time.Second
	for range k / 3 {
		time.Sleep(time.Millisecond * 50)

		resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody1))
		require.NoError(t, err)

		func() {
			defer resp.Body.Close()

			var got []CrawlResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
			require.Len(t, got, 1)
			require.Equal(t, urls1[0], got[0].URL)
		}()
	}

	for range k / 3 {
		time.Sleep(time.Millisecond * 50)

		resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody2))
		require.NoError(t, err)

		func() {
			defer resp.Body.Close()

			var got []CrawlResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
			require.Len(t, got, 1)
			require.Equal(t, urls2[0], got[0].URL)
		}()
	}

	for range k / 3 {
		time.Sleep(time.Millisecond * 50)

		resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody3))
		require.NoError(t, err)

		func() {
			defer resp.Body.Close()

			var got []CrawlResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
			require.Len(t, got, 1)
			require.Equal(t, urls3[0], got[0].URL)
		}()
	}

	require.EqualValues(t, 1, hits.Load())
	time.Sleep(cacheTTL + time.Millisecond*100)

	resp, err := c.Post(p, contentTypeJson, bytes.NewReader(reqBody1))
	require.NoError(t, err)

	func() {
		defer resp.Body.Close()

		var got []CrawlResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got, 1)
		require.Equal(t, urls1[0], got[0].URL)
	}()

	require.EqualValues(t, 2, hits.Load())
}

func TestDoFetchClosesBody(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	httpClient := client()

	down, conns := newConnCountingServer(t)
	t.Cleanup(down.Close)

	const (
		n       = 100
		workers = 2
	)
	urls := makeURLs(t, down.URL, 100)

	reqBody, err := json.Marshal(CrawlRequest{
		URLs:      urls,
		Workers:   workers,
		TimeoutMS: 5000,
	})
	require.NoError(t, err)

	resp, err := httpClient.Post(p, contentTypeJson, bytes.NewReader(reqBody))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []CrawlResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, n)

	for i := range got {
		require.Empty(t, got[i].Error)
		require.Equal(t, http.StatusOK, got[i].StatusCode)
	}

	require.LessOrEqual(t, conns.Load(), int64(workers*2),
		"expected connection reuse, too many new TCP conns")

	// hint: Подумайте о том, в каких ситуациях переиспользуются соединения

	// hint+:
	// defer resp.Body.Close()
	// _, _ = io.Copy(io.Discard, resp.Body) // for reusing connection
}

func TestListenAndServeOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	t.Cleanup(cancel)

	baseUrl, stopWait := startCrawlerServer(ctx, t)
	cancel()
	stopWait()

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	_, err := c.Get(p)

	if runtime.GOOS == "windows" {
		require.ErrorContains(t, err, "No connection could be made")
		return
	}

	require.ErrorContains(t, err, "connection refused")
}

// TestListenAndServeShutdownOnContextCancel
// При отмене контекста хотим аккуратно завершить текущие запросы
func TestListenAndServeShutdownOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	baseUrl, stopWait := startCrawlerServer(ctx, t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	var (
		hits atomic.Int64
		wg   sync.WaitGroup
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer wg.Done()

		time.Sleep(time.Second)

		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	const n = 27
	wg.Add(n)
	urls := makeURLs(t, srv.URL, n)

	reqBody, _ := json.Marshal(CrawlRequest{
		URLs:      urls,
		Workers:   n,
		TimeoutMS: 30_000,
	})

	wg.Go(func() {
		req, err := http.NewRequest(http.MethodPost, p, bytes.NewReader(reqBody))
		require.NoError(t, err)

		resp, err := c.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		t.Cleanup(func() {
			resp.Body.Close()
		})

		var got []CrawlResponse
		err = json.NewDecoder(resp.Body).Decode(&got)
		require.NoError(t, err)

		require.Equal(t, len(urls), len(got))

		for i := range urls {
			require.Equal(t, urls[i], got[i].URL)
			require.Empty(t, got[i].Error)
			require.Equal(t, http.StatusNoContent, got[i].StatusCode)
		}
	})

	time.Sleep(time.Millisecond * 300)
	cancel()

	wg.Wait()

	require.Equal(t, int64(len(urls)), hits.Load())

	// hint: use http.Server{}.ShutDown()

	_, err := c.Get(p)

	if runtime.GOOS == "windows" {
		require.ErrorContains(t, err, "No connection could be made")
		return
	}

	require.ErrorContains(t, err, "connection refused")
}
