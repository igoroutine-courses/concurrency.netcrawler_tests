//go:build performance_test

package crawler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCrawlerSleepPerformance(t *testing.T) {
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
		time.Sleep(time.Second * 3)

		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	const n = 100
	wg.Add(n)
	urls := makeURLs(t, srv.URL, n)

	reqBody, _ := json.Marshal(CrawlRequest{
		URLs:      urls,
		Workers:   n,
		TimeoutMS: 3000000,
	})

	req, err := http.NewRequest(http.MethodPost, p, bytes.NewReader(reqBody))
	require.NoError(t, err)

	start := time.Now()
	resp, err := c.Do(req)
	delay := time.Since(start)

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

	require.LessOrEqual(t, delay, time.Second*6)
}
