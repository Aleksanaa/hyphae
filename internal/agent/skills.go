package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is a global on-demand capability package discovered under the skills
// directory. Only Name and Description are surfaced in the system prompt (see
// FormatSkills); the full instructions live in the markdown file at Path and are
// loaded on demand — either by the model via read_file, or force-injected through
// SkillReminder.
type Skill struct {
	Name        string
	Description string
	Path        string // the skill's markdown file (SKILL.md, or a single-file *.md)
	Dir         string // directory that the skill's relative paths resolve against
}

// LoadSkills scans dir for skills. Discovery mirrors the Agent Skills layout:
//   - a subdirectory containing SKILL.md is one skill (not recursed into further);
//   - a top-level *.md file is a single-file skill;
//   - other subdirectories are recursed to find nested SKILL.md skills.
//
// Entries that cannot be read or lack a usable name/description are skipped. A
// missing directory yields nil.
func LoadSkills(dir string) []Skill {
	var out []Skill
	loadSkillsInto(&out, dir, true)
	return out
}

func loadSkillsInto(out *[]Skill, dir string, root bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() {
			skillFile := filepath.Join(full, "SKILL.md")
			if info, err := os.Stat(skillFile); err == nil && !info.IsDir() {
				if s, ok := loadSkillFile(skillFile, name); ok {
					*out = append(*out, s)
				}
				continue // a skill root is not recursed into further
			}
			loadSkillsInto(out, full, false)
			continue
		}
		// Root-level single-file skills only.
		if root && strings.HasSuffix(name, ".md") {
			if s, ok := loadSkillFile(full, strings.TrimSuffix(name, ".md")); ok {
				*out = append(*out, s)
			}
		}
	}
}

func loadSkillFile(path, fallbackName string) (Skill, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, false
	}
	name, desc, body := parseSkillFrontmatter(string(data))
	if name == "" {
		name = fallbackName
	}
	if desc == "" {
		desc = firstNonEmptyLine(body)
	}
	if name == "" || desc == "" {
		return Skill{}, false
	}
	return Skill{Name: name, Description: desc, Path: path, Dir: filepath.Dir(path)}, true
}

// parseSkillFrontmatter extracts name/description from a leading "---"-fenced
// YAML frontmatter block and returns the remaining body. When there is no
// frontmatter it returns empty name/description and the whole content as body.
func parseSkillFrontmatter(content string) (name, desc, body string) {
	s := strings.TrimPrefix(content, string(rune(0xFEFF))) // strip UTF-8 BOM
	if !strings.HasPrefix(s, "---") {
		return "", "", content
	}
	rest := s[len("---"):]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return "", "", content
	}
	rest = rest[nl+1:] // drop the remainder of the opening fence line
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", "", content
	}
	fm := rest[:end]
	after := rest[end+1:] // starts at the closing "---" line
	if i := strings.IndexByte(after, '\n'); i >= 0 {
		body = after[i+1:]
	}
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	_ = yaml.Unmarshal([]byte(fm), &meta) // best-effort; malformed frontmatter falls back to defaults
	return meta.Name, meta.Description, body
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line != "" {
			return line
		}
	}
	return ""
}

// FormatSkills renders the <available_skills> block appended to the system
// prompt. Only names/descriptions/locations are included; this is progressive
// disclosure — the model reads a skill's file when a task matches. Empty input
// yields an empty string.
func FormatSkills(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nThe following skills provide specialized instructions for specific tasks. ")
	b.WriteString("When a task matches a skill's description, use read_file to load its file, then follow it. ")
	b.WriteString("Resolve any relative paths a skill mentions against its <dir>.\n")
	b.WriteString("<available_skills>\n")
	for _, s := range skills {
		b.WriteString("  <skill>\n")
		fmt.Fprintf(&b, "    <name>%s</name>\n", escapeXML(s.Name))
		fmt.Fprintf(&b, "    <description>%s</description>\n", escapeXML(s.Description))
		fmt.Fprintf(&b, "    <location>%s</location>\n", escapeXML(s.Path))
		fmt.Fprintf(&b, "    <dir>%s</dir>\n", escapeXML(s.Dir))
		b.WriteString("  </skill>\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}

// ReadSkillBody reads a skill file and returns its instruction body with the
// frontmatter stripped, for force-loading via SkillReminder.
func ReadSkillBody(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	_, _, body := parseSkillFrontmatter(string(data))
	return body, nil
}

// SkillReminder wraps a skill's loaded body as a one-shot <skill> block, for the
// palette force-load path. Delivered via Session.AddReminder so it rides the next
// user message.
func SkillReminder(s Skill, body string) string {
	block := fmt.Sprintf("<skill name=%q location=%q dir=%q>\nReferences are relative to %s.\n\n%s\n</skill>",
		s.Name, s.Path, s.Dir, s.Dir, strings.TrimSpace(body))
	return SystemReminder(block)
}

// SkillUnloadReminder tells the model a previously-loaded skill has been
// unloaded and should no longer be used. For the palette unload path.
func SkillUnloadReminder(s Skill) string {
	return SystemReminder(fmt.Sprintf(
		"The %q skill has been unloaded. Disregard its instructions and do not use it for the rest of this conversation unless it is loaded again.",
		s.Name))
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
