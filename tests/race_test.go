//go:build race_test

package crawler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCrawlRace(t *testing.T) {
	baseUrl, stopWait := startCrawlerServer(t.Context(), t)
	t.Cleanup(stopWait)

	p := constructCrawlPath(t, baseUrl).String()
	c := client()

	var (
		hits atomic.Int64
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	t.Cleanup(func() {
		srv.Close()
	})

	const n = 27
	urls := makeURLs(t, srv.URL, n)

	for range n {
		urls = append(urls, srv.URL)
	}

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
}
