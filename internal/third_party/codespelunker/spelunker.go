// Package codespelunker provides ranked code search extracted from
// github.com/boyter/cs (code spelunker). The MCP server, TUI, HTTP server,
// CLI, cache, and color-output layers have been stripped; only the search
// pipeline, ranking, and line-result extraction are kept.
package codespelunker

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aleksanaa/hyphae/internal/third_party/codespelunker/common"
	"github.com/aleksanaa/hyphae/internal/third_party/codespelunker/search"
	"github.com/aleksanaa/hyphae/internal/third_party/codespelunker/snippet"
	"github.com/boyter/gocodewalker"
	"github.com/boyter/scc/v3/processor"
)

var initOnce sync.Once

func initDB() {
	initOnce.Do(processor.ProcessConstants)
}

// Options controls the search behavior.
type Options struct {
	CaseSensitive bool
	MaxReadBytes  int64    // 0 = default (1 MB)
	ExcludeDirs   []string // dirs to exclude; default: .git, .hg, .svn
}

// Search runs a ranked code search in dir using a boolean query.
// Query supports: AND (implicit), OR, NOT, "phrases", /regex/, fuzzy~1,
// and filters like ext:go, lang:Python, path:pkg, file:test.
// Returns matched files sorted by score descending, plus total files scanned.
func Search(ctx context.Context, query, dir string, opts Options) ([]*common.FileJob, int, error) {
	initDB()

	lexer := search.NewLexer(strings.NewReader(query))
	parser := search.NewParser(lexer)
	ast, _ := parser.ParseQuery()
	if ast == nil {
		return nil, 0, nil
	}
	tr := &search.Transformer{}
	ast, _ = tr.TransformAST(ast)
	ast = search.PlanAST(ast)

	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	dir, _ = filepath.Abs(dir)

	maxRead := opts.MaxReadBytes
	if maxRead == 0 {
		maxRead = 1_000_000
	}
	excludeDirs := opts.ExcludeDirs
	if len(excludeDirs) == 0 {
		excludeDirs = []string{".git", ".hg", ".svn"}
	}

	fileQueue := make(chan *gocodewalker.File, 1000)
	walker := gocodewalker.NewParallelFileWalker([]string{dir}, fileQueue)
	walker.ExcludeDirectory = excludeDirs

	searchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			walker.Terminate()
		case <-searchDone:
		}
	}()
	go func() { _ = walker.Start() }()

	out := make(chan *common.FileJob, runtime.NumCPU())
	var fileCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var poolBuf []byte
			if v := bufPool.Get(); v != nil {
				poolBuf = v.([]byte)
			}
			if int64(len(poolBuf)) < maxRead {
				poolBuf = make([]byte, maxRead)
			}
			defer bufPool.Put(poolBuf)

			for f := range fileQueue {
				select {
				case <-ctx.Done():
					return
				default:
				}

				fileCount.Add(1)

				content, err := readFileBuf(f.Location, poolBuf[:maxRead])
				if err != nil || len(content) == 0 {
					continue
				}

				// Skip binary files (NUL byte in first 10 KB)
				check := content
				if len(check) > 10_000 {
					check = content[:10_000]
				}
				if bytes.IndexByte(check, 0) != -1 {
					continue
				}

				matched, matchLocations := search.EvaluateFile(ast, content, f.Filename, f.Location, opts.CaseSensitive)
				if !matched {
					continue
				}

				// Copy content out of the pool buffer before touching metadata
				owned := make([]byte, len(content))
				copy(owned, content)

				lang, sccLines, sccCode, sccComment, sccBlank, sccComplexity, contentByteType := fileCodeStats(f.Filename, owned)

				if !search.PostEvalMetadataFilters(ast, lang, sccComplexity) {
					continue
				}

				snippet.AddPhraseMatchLocations(owned, strings.Trim(query, "\""), matchLocations)

				fj := &common.FileJob{
					Filename:        f.Filename,
					Extension:       gocodewalker.GetExtension(f.Filename),
					Location:        f.Location,
					Content:         owned,
					ContentByteType: contentByteType,
					Bytes:           len(owned),
					MatchLocations:  matchLocations,
					Language:        lang,
					Lines:           sccLines,
					Code:            sccCode,
					Comment:         sccComment,
					Blank:           sccBlank,
					Complexity:      sccComplexity,
				}

				select {
				case out <- fj:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(out)
		close(searchDone)
	}()

	var results []*common.FileJob
	for fj := range out {
		results = append(results, fj)
	}

	return results, int(fileCount.Load()), nil
}

var bufPool sync.Pool

func readFileBuf(location string, buf []byte) ([]byte, error) {
	f, err := os.Open(location)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, err := io.ReadFull(f, buf)
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if n == 0 {
				return nil, nil
			}
			return buf[:n], nil
		}
		return nil, err
	}
	return buf[:n], nil
}

// fileCodeStats detects the language and computes SCC code statistics.
// Returns zero/empty values for unrecognised files.
func fileCodeStats(filename string, content []byte) (language string, lines, code, comment, blank, complexity int64, contentByteType []byte) {
	detected, _ := processor.DetectLanguage(filename)
	if len(detected) == 0 {
		return
	}
	if len(detected) >= 2 {
		language = processor.DetermineLanguage(filename, detected[0], detected, content)
	} else {
		language = detected[0]
	}
	if language == "" {
		return
	}
	sccJob := &processor.FileJob{
		Filename:        filename,
		Language:        language,
		Content:         content,
		Bytes:           int64(len(content)),
		ClassifyContent: true,
	}
	processor.CountStats(sccJob)
	return language, sccJob.Lines, sccJob.Code, sccJob.Comment, sccJob.Blank, sccJob.Complexity, sccJob.ContentByteType
}
