package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	ddgURL        = "https://html.duckduckgo.com/html/"
	ddgMaxResults = 10
	ddgTimeout    = 15
)

type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

func duckduckgoSearch(ctx context.Context, query string, maxResults int) ([]searchResult, error) {
	if maxResults <= 0 {
		maxResults = ddgMaxResults
	}

	ctx, cancel := context.WithTimeout(ctx, ddgTimeout*time.Second)
	defer cancel()

	form := url.Values{}
	form.Set("q", query)
	form.Set("kl", "us-en")

	req, err := http.NewRequestWithContext(ctx, "POST", ddgURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", fetchUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	return parseDDGResults(string(body), maxResults), nil
}

func parseDDGResults(body string, max int) []searchResult {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}

	var results []searchResult

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= max {
			return
		}
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "result__body") {
			r := extractResult(n)
			if r.Title != "" && r.URL != "" {
				results = append(results, r)
			}
			return // don't recurse into result__body
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return results
}

func extractResult(n *html.Node) searchResult {
	var r searchResult
	var walk func(*html.Node)
	walk = func(m *html.Node) {
		if m.Type == html.ElementNode {
			if m.Data == "a" && hasClass(m, "result__a") {
				r.Title = nodeText(m)
				for _, a := range m.Attr {
					if a.Key == "href" {
						r.URL = cleanDDGURL(a.Val)
					}
				}
			} else if hasClass(m, "result__snippet") {
				r.Snippet = nodeText(m)
			}
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return r
}

// cleanDDGURL strips DDG redirect wrappers if present.
func cleanDDGURL(raw string) string {
	if strings.HasPrefix(raw, "//duckduckgo.com/l/") {
		if u, err := url.Parse("https:" + raw); err == nil {
			if dest := u.Query().Get("uddg"); dest != "" {
				if decoded, err := url.QueryUnescape(dest); err == nil {
					return decoded
				}
				return dest
			}
		}
	}
	return raw
}

func hasClass(n *html.Node, cls string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == cls {
					return true
				}
			}
		}
	}
	return false
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(m *html.Node) {
		if m.Type == html.TextNode {
			b.WriteString(m.Data)
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}
