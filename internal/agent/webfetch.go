package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	maxFetchBytes    = 5 * 1024 * 1024
	defaultFetchSecs = 30
	maxFetchSecs     = 120
	fetchUA          = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
)

func fetchURL(ctx context.Context, rawURL, format string, timeoutSecs int) (string, error) {
	if timeoutSecs <= 0 {
		timeoutSecs = defaultFetchSecs
	} else if timeoutSecs > maxFetchSecs {
		timeoutSecs = maxFetchSecs
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", fetchUA)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept", fetchAccept(format))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxFetchBytes+1)))
	if err != nil {
		return "", err
	}
	if len(body) > maxFetchBytes {
		return "", fmt.Errorf("response exceeds %d byte limit", maxFetchBytes)
	}

	content := string(body)
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/html") {
		switch format {
		case "markdown":
			return convertHTMLToMarkdown(content), nil
		case "text":
			return extractHTMLText(content), nil
		}
	}
	return content, nil
}

func fetchAccept(format string) string {
	switch format {
	case "markdown":
		return "text/markdown;q=1.0, text/html;q=0.7, */*;q=0.1"
	case "text":
		return "text/plain;q=1.0, text/html;q=0.8, */*;q=0.1"
	case "html":
		return "text/html;q=1.0, */*;q=0.1"
	}
	return "*/*"
}

// extractHTMLText strips HTML, skipping noise tags, and returns plain text.
func extractHTMLText(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return src
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "iframe", "object", "embed", "head":
				return
			}
		}
		if n.Type == html.TextNode {
			s := strings.TrimSpace(n.Data)
			if s != "" {
				b.WriteString(s)
				b.WriteByte('\n')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return strings.TrimSpace(b.String())
}

// convertHTMLToMarkdown converts an HTML document to Markdown.
func convertHTMLToMarkdown(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return src
	}
	c := &mdConverter{}
	c.walk(doc)
	// Collapse runs of more than two consecutive blank lines.
	lines := strings.Split(c.b.String(), "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
			if blank <= 2 {
				out = append(out, "")
			}
		} else {
			blank = 0
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

type mdConverter struct {
	b         strings.Builder
	listStack []bool // true = ordered
	listCount []int
	inPre     bool
	skipDepth int
}

var mdNoiseTags = map[string]bool{
	"script": true, "style": true, "noscript": true,
	"iframe": true, "object": true, "embed": true, "head": true,
}

func (c *mdConverter) attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// nl ensures the buffer ends with a newline.
func (c *mdConverter) nl() {
	s := c.b.String()
	if len(s) > 0 && s[len(s)-1] != '\n' {
		c.b.WriteByte('\n')
	}
}

// blank ensures there is a blank line before new content.
func (c *mdConverter) blank() {
	s := c.b.String()
	if len(s) == 0 {
		return
	}
	if strings.HasSuffix(s, "\n\n") {
		return
	}
	if strings.HasSuffix(s, "\n") {
		c.b.WriteByte('\n')
	} else {
		c.b.WriteString("\n\n")
	}
}

func (c *mdConverter) walkChildren(n *html.Node) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch)
	}
}

func (c *mdConverter) heading(n *html.Node, prefix string) {
	c.blank()
	c.b.WriteString(prefix)
	c.walkChildren(n)
	c.blank()
}

func (c *mdConverter) walk(n *html.Node) {
	switch n.Type {
	case html.DocumentNode:
		c.walkChildren(n)
		return

	case html.TextNode:
		if c.skipDepth > 0 {
			return
		}
		s := n.Data
		if !c.inPre {
			s = strings.Join(strings.Fields(s), " ")
		}
		if s != "" {
			c.b.WriteString(s)
		}
		return

	case html.ElementNode:
		if c.skipDepth > 0 {
			c.skipDepth++
			c.walkChildren(n)
			c.skipDepth--
			return
		}
		tag := n.Data
		if mdNoiseTags[tag] {
			c.skipDepth++
			c.walkChildren(n)
			c.skipDepth--
			return
		}

		switch tag {
		case "h1":
			c.heading(n, "# ")
		case "h2":
			c.heading(n, "## ")
		case "h3":
			c.heading(n, "### ")
		case "h4":
			c.heading(n, "#### ")
		case "h5":
			c.heading(n, "##### ")
		case "h6":
			c.heading(n, "###### ")

		case "p":
			c.blank()
			c.walkChildren(n)
			c.blank()

		case "strong", "b":
			c.b.WriteString("**")
			c.walkChildren(n)
			c.b.WriteString("**")

		case "em", "i":
			c.b.WriteString("*")
			c.walkChildren(n)
			c.b.WriteString("*")

		case "code":
			if c.inPre {
				c.walkChildren(n)
			} else {
				c.b.WriteString("`")
				c.walkChildren(n)
				c.b.WriteString("`")
			}

		case "pre":
			c.blank()
			lang := ""
			if fc := n.FirstChild; fc != nil && fc.Type == html.ElementNode && fc.Data == "code" {
				cls := c.attr(fc, "class")
				for _, part := range strings.Fields(cls) {
					if strings.HasPrefix(part, "language-") {
						lang = strings.TrimPrefix(part, "language-")
						break
					}
				}
			}
			c.b.WriteString("```")
			c.b.WriteString(lang)
			c.b.WriteByte('\n')
			c.inPre = true
			c.walkChildren(n)
			c.inPre = false
			c.nl()
			c.b.WriteString("```")
			c.blank()

		case "a":
			href := c.attr(n, "href")
			c.b.WriteString("[")
			c.walkChildren(n)
			c.b.WriteString("](")
			c.b.WriteString(href)
			c.b.WriteString(")")

		case "img":
			alt := c.attr(n, "alt")
			src := c.attr(n, "src")
			c.b.WriteString(fmt.Sprintf("![%s](%s)", alt, src))

		case "ul":
			c.blank()
			c.listStack = append(c.listStack, false)
			c.listCount = append(c.listCount, 0)
			c.walkChildren(n)
			c.listStack = c.listStack[:len(c.listStack)-1]
			c.listCount = c.listCount[:len(c.listCount)-1]
			c.blank()

		case "ol":
			c.blank()
			c.listStack = append(c.listStack, true)
			c.listCount = append(c.listCount, 0)
			c.walkChildren(n)
			c.listStack = c.listStack[:len(c.listStack)-1]
			c.listCount = c.listCount[:len(c.listCount)-1]
			c.blank()

		case "li":
			c.nl()
			depth := len(c.listStack)
			if depth == 0 {
				// <li> outside any <ul>/<ol> — malformed HTML; treat as unordered.
				c.b.WriteString("- ")
				c.walkChildren(n)
				c.nl()
				break
			}
			indent := strings.Repeat("  ", depth-1)
			if c.listStack[depth-1] {
				c.listCount[depth-1]++
				c.b.WriteString(fmt.Sprintf("%s%d. ", indent, c.listCount[depth-1]))
			} else {
				c.b.WriteString(indent + "- ")
			}
			c.walkChildren(n)
			c.nl()

		case "blockquote":
			c.blank()
			var inner mdConverter
			inner.walkChildren(n)
			for _, line := range strings.Split(strings.TrimSpace(inner.b.String()), "\n") {
				c.b.WriteString("> ")
				c.b.WriteString(line)
				c.b.WriteByte('\n')
			}
			c.blank()

		case "hr":
			c.blank()
			c.b.WriteString("---")
			c.blank()

		case "br":
			c.b.WriteByte('\n')

		// Table: very basic — no header separator
		case "tr":
			c.nl()
			c.b.WriteString("|")
			c.walkChildren(n)
			c.nl()

		case "td", "th":
			c.b.WriteString(" ")
			c.walkChildren(n)
			c.b.WriteString(" |")

		default:
			c.walkChildren(n)
		}

	default:
		c.walkChildren(n)
	}
}
