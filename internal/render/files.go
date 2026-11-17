package render

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// IncludeDir is the subdirectory, relative to bird.conf, that birdy writes its
// per-section files into. birdy owns this directory exclusively: every apply
// rewrites the files the model needs and removes any it no longer does.
const IncludeDir = "birdy.d"

// File is one file birdy writes under the config directory. Name is a POSIX
// path relative to that directory, e.g. "birdy.d/09-peers-edge1.conf".
type File struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

// FileSet is the rendered config laid out across several files: the entry
// bird.conf (a short banner plus a list of includes) and one include file per
// section. Concatenating Includes[i].Body in order reproduces Config(in)
// byte-for-byte, so the logical config birdy hashes and diffs is independent of
// how it is split on disk. Entry itself is layout metadata (its bytes carry the
// absolute include paths) and is deliberately not part of that logical config.
type FileSet struct {
	Entry    string `json:"entry"`
	Includes []File `json:"includes"`
}

// Logical returns the concatenation of the include bodies — what BIRD parses
// once the includes are spliced in, and equal to Config(in).
func (fs FileSet) Logical() string {
	var b strings.Builder
	for _, f := range fs.Includes {
		b.WriteString(f.Body)
	}
	return b.String()
}

// groupNum numbers the top-level section groups so an operator listing birdy.d/
// sees related files together (all peers, all sets). The number is stable per
// group, so adding a peer or a set never renumbers unrelated files. The actual
// load order is set by the ordered include lines in bird.conf, not by these
// names, so grouping does not have to match load order exactly.
var groupNum = map[string]string{
	"header":      "00",
	"globals":     "01",
	"communities": "02",
	"sets":        "03",
	"bogons":      "04",
	"rpki":        "05",
	"protocols":   "06",
	"static":      "07",
	"policies":    "08",
	"templates":   "08", // with the policies: what peers are declared "from" comes before them
	"peers":       "09",
	"raw":         "10",
}

// includeFileName maps a section path to its file under IncludeDir, e.g.
// "peers/edge1" -> "birdy.d/09-peers-edge1.conf".
func includeFileName(sectionPath string) string {
	group := sectionPath
	if i := strings.IndexByte(sectionPath, '/'); i >= 0 {
		group = sectionPath[:i]
	}
	num, ok := groupNum[group]
	if !ok {
		num = "99"
	}
	slug := strings.ReplaceAll(sectionPath, "/", "-")
	return IncludeDir + "/" + num + "-" + slug + ".conf"
}

// includeNameRe is the exact shape includeFileName produces: the include
// directory, a two-digit group number, and the section path with its slashes
// turned into dashes. Every part of a section path is either a fixed word
// ("sets/prefixes") or a model name, which the store limits to BIRD identifier
// characters — so letters, digits, underscore and the joining dashes are all
// that can ever appear.
var includeNameRe = regexp.MustCompile(`^` + regexp.QuoteMeta(IncludeDir) + `/[0-9]{2}(-[A-Za-z0-9_]+)+\.conf$`)

// ValidIncludeName reports whether name is an include file name this renderer
// could have produced. A file set is also read back from the database (a stored
// version, a pending apply) and its names become paths birdy writes to, so a
// database restored from a crafted backup must not be able to name a file
// outside birdy.d — or anything else the renderer would never write. The
// pattern alone excludes "..", absolute paths, backslashes and subdirectories;
// filepath.IsLocal is checked as well so the guarantee does not rest on reading
// a regular expression correctly.
func ValidIncludeName(name string) error {
	if !includeNameRe.MatchString(name) || !filepath.IsLocal(filepath.FromSlash(name)) {
		return fmt.Errorf("render: %q is not an include file birdy writes (want %s/NN-section.conf)", name, IncludeDir)
	}
	return nil
}

// Verify checks a file set that was read back from storage before it is trusted
// for a write: cfg is the logical config stored beside it — the text `bird -p`
// checks and the operator is shown — and dir is the directory bird.conf lives in.
//
// What gets written must be what was checked. So every include must carry a
// name the renderer produces, the include bodies must add up to cfg, and the
// entry file must be nothing but comments and one include line per file, in
// order, pointing into dir. A set with no includes is the single-file layout
// older versions stored: its entry is the config itself.
func (fs FileSet) Verify(cfg, dir string) error {
	if len(fs.Includes) == 0 {
		if fs.Entry != cfg {
			return errors.New("render: the stored bird.conf differs from the stored config text")
		}
		return nil
	}
	seen := make(map[string]bool, len(fs.Includes))
	for _, f := range fs.Includes {
		if err := ValidIncludeName(f.Name); err != nil {
			return err
		}
		if seen[f.Name] {
			return fmt.Errorf("render: include file %q is listed twice", f.Name)
		}
		seen[f.Name] = true
	}
	if fs.Logical() != cfg {
		return errors.New("render: the stored include files do not add up to the stored config text")
	}
	// The entry is not part of the logical config, so nothing above covers it,
	// yet BIRD parses every line of it. Hold it to what Files writes.
	base := entryBase(dir)
	next := 0
	for _, line := range strings.Split(fs.Entry, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if next >= len(fs.Includes) || line != entryIncludeLine(base, fs.Includes[next].Name) {
			return fmt.Errorf("render: the stored bird.conf has a line birdy does not write there: %q", line)
		}
		next++
	}
	if next != len(fs.Includes) {
		return errors.New("render: the stored bird.conf does not include every stored file")
	}
	return nil
}

// entryBase is dir as it appears in the entry file's include paths.
func entryBase(dir string) string {
	base := strings.TrimRight(strings.ReplaceAll(dir, "\\", "/"), "/")
	if base == "" {
		base = "."
	}
	return base
}

// entryIncludeLine is the one statement the entry file carries per include.
func entryIncludeLine(base, name string) string {
	return fmt.Sprintf("include \"%s/%s\";", base, name)
}

// Files lays the rendered config out as a bird.conf that includes one file per
// section under IncludeDir. dir is the absolute directory bird.conf lives in,
// used to build absolute include paths — absolute so BIRD resolves them the same
// no matter what its working directory or relative-include rules are. The result
// is a pure function of (in, dir).
func Files(in Input, dir string) (FileSet, error) {
	secs, err := Sections(in)
	if err != nil {
		return FileSet{}, err
	}
	base := entryBase(dir)

	var entry strings.Builder
	writeEntryHeader(&entry, in.Version)
	includes := make([]File, 0, len(secs))
	for _, s := range secs {
		name := includeFileName(s.Path)
		entry.WriteString(entryIncludeLine(base, name) + "\n")
		includes = append(includes, File{Name: name, Body: s.Body})
	}
	return FileSet{Entry: entry.String(), Includes: includes}, nil
}

func writeEntryHeader(b *strings.Builder, version string) {
	fmt.Fprintf(b, `#
# bird.conf generated by birdy %s
#
# birdy owns this file and the %s/ directory beside it, and rewrites them in
# full from its database on every apply. This file only pulls in the generated
# sections below; change the model in the birdy UI, not the files — hand edits
# are lost on the next apply.
#

`, version, IncludeDir)
}
