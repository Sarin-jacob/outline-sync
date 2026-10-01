package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	navMarker      = "<!-- OUTLINE_SYNC_NAV -->"
	childrenMarker = "<!-- OUTLINE_SYNC_NESTED_LINKS -->" // same marker as the Python version
)

// ClearWorkTree deletes everything in dir except .git, so documents removed in
// Outline are removed from the repo too.
func ClearWorkTree(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Extract unpacks a zip into dest, refusing entries that would escape it.
func Extract(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open export: %w", err)
	}
	defer zr.Close()

	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()

	for _, f := range zr.File {
		name := filepath.FromSlash(f.Name)
		if !filepath.IsLocal(name) || strings.HasPrefix(filepath.ToSlash(name), ".git/") || name == ".git" {
			return fmt.Errorf("export contains unsafe path %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
			continue
		}
		if dir := filepath.Dir(name); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		if err := extractFile(root, f, name); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(root *os.Root, f *zip.File, name string) error {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

// FindCollectionDir returns the folder holding the collection's documents.
// Outline names it after the collection, but it may sanitise the name, so if
// the exact name is missing fall back to the single top-level folder.
func FindCollectionDir(repoDir, collection string) string {
	exact := filepath.Join(repoDir, collection)
	if fi, err := os.Stat(exact); err == nil && fi.IsDir() {
		return exact
	}
	entries, _ := os.ReadDir(repoDir)
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != ".git" {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 1 {
		return filepath.Join(repoDir, dirs[0])
	}
	return repoDir
}

// doc is one markdown file in the exported tree.
type doc struct {
	Title    string
	Path     string // absolute path of the .md file
	Children []*doc
}

// buildTree loads the document hierarchy under dir. A document "Foo.md" owns
// the children found in the sibling folder "Foo/".
func buildTree(dir string, top bool) []*doc {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var docs []*doc
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.EqualFold(filepath.Ext(name), ".md") {
			continue
		}
		if top && strings.EqualFold(name, "README.md") {
			continue // generated index (or a doc we would overwrite anyway)
		}
		title := name[:len(name)-len(filepath.Ext(name))]
		d := &doc{Title: title, Path: filepath.Join(dir, name)}
		if fi, err := os.Stat(filepath.Join(dir, title)); err == nil && fi.IsDir() {
			d.Children = buildTree(filepath.Join(dir, title), false)
		}
		docs = append(docs, d)
	}
	slices.SortFunc(docs, func(a, b *doc) int {
		if c := strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)); c != 0 {
			return c
		}
		return strings.Compare(a.Title, b.Title)
	})
	return docs
}

// AddNavigation adds a breadcrumb trail to the top of every document and, at
// the bottom, a list of its sub-pages plus a link back to its parent.
func AddNavigation(collectionDir, readmePath, collection string) error {
	home := crumb{Title: "🏠 " + collection, Path: readmePath}
	var errs []error
	var walk func(docs []*doc, trail []crumb)
	walk = func(docs []*doc, trail []crumb) {
		for _, d := range docs {
			if err := decorate(d, trail); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", d.Path, err))
			}
			walk(d.Children, append(slices.Clip(trail), crumb{Title: d.Title, Path: d.Path}))
		}
	}
	walk(buildTree(collectionDir, collectionDir == filepath.Dir(readmePath)), []crumb{home})
	if len(errs) > 0 {
		return fmt.Errorf("navigation: %d file(s) failed, first: %w", len(errs), errs[0])
	}
	return nil
}

type crumb struct {
	Title string
	Path  string
}

func decorate(d *doc, trail []crumb) error {
	raw, err := os.ReadFile(d.Path)
	if err != nil {
		return err
	}
	content := string(raw)
	if strings.Contains(content, navMarker) || strings.Contains(content, childrenMarker) {
		return nil // already processed
	}
	from := filepath.Dir(d.Path)

	var top strings.Builder
	top.WriteString(navMarker + "\n")
	for _, c := range trail {
		fmt.Fprintf(&top, "[%s](%s) › ", escapeLinkText(c.Title), relLink(from, c.Path))
	}
	fmt.Fprintf(&top, "**%s**\n\n---\n\n", escapeLinkText(d.Title))

	var bottom strings.Builder
	bottom.WriteString("\n\n" + childrenMarker + "\n\n---\n\n")
	if len(d.Children) > 0 {
		bottom.WriteString("**Sub-pages**\n\n")
		for _, c := range d.Children {
			fmt.Fprintf(&bottom, "* [%s](%s)\n", escapeLinkText(c.Title), relLink(from, c.Path))
		}
		bottom.WriteString("\n")
	}
	parent := trail[len(trail)-1]
	fmt.Fprintf(&bottom, "[⬆ Back to %s](%s)\n", escapeLinkText(strings.TrimPrefix(parent.Title, "🏠 ")), relLink(from, parent.Path))

	out := top.String() + strings.TrimRight(content, "\r\n") + bottom.String()
	return os.WriteFile(d.Path, []byte(out), 0o644)
}

// WriteReadme generates the repository index with the full document tree.
func WriteReadme(readmePath, collectionDir, collection string, now time.Time) error {
	docs := buildTree(collectionDir, collectionDir == filepath.Dir(readmePath))
	from := filepath.Dir(readmePath)

	var b strings.Builder
	fmt.Fprintf(&b, "# %s Index\n\n", collection)
	fmt.Fprintf(&b, "Last synced: %s\n\n## Documents\n\n", now.Format("2006-01-02 15:04:05 MST"))
	if len(docs) == 0 {
		b.WriteString("*No documents found.*\n")
	}
	var walk func(docs []*doc, depth int)
	walk = func(docs []*doc, depth int) {
		for _, d := range docs {
			fmt.Fprintf(&b, "%s* [%s](%s)\n", strings.Repeat("  ", depth), escapeLinkText(d.Title), relLink(from, d.Path))
			walk(d.Children, depth+1)
		}
	}
	walk(docs, 0)
	return os.WriteFile(readmePath, []byte(b.String()), 0o644)
}

// relLink returns a URL-encoded relative link from directory `from` to `to`.
func relLink(from, to string) string {
	rel, err := filepath.Rel(from, to)
	if err != nil {
		rel = to
	}
	return quotePath(filepath.ToSlash(rel))
}

// quotePath percent-encodes everything except unreserved characters and '/',
// matching Python's urllib.parse.quote so links stay identical to before.
func quotePath(p string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_.-~/", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

var linkTextEscaper = strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`, `*`, `\*`, `_`, `\_`)

func escapeLinkText(s string) string { return linkTextEscaper.Replace(s) }
