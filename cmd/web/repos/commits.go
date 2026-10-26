package repos

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/v91/github"
)

var (
	// A Conventional Commits header: a type, an optional scope, an optional !, then ": " and a description starting
	// right after it.
	headerPattern = regexp.MustCompile(`^([A-Za-z]+)(?:\(([^()\n]+)\))?(!)?: (\S.*)$`)

	// A footer line: a token of letters, digits, and -, or BREAKING CHANGE in any case, then ": " or " #".
	footerPattern = regexp.MustCompile(`^([A-Za-z0-9-]+|(?i:BREAKING CHANGE))(: | #)(.*)$`)
)

// Parses a comparison commit, timestamped by its committer date.
func fromComparisonCommit(repo *github.PushEventRepository, c *github.RepositoryCommit) (ConventionalCommit, bool) {
	commit := c.GetCommit()
	return parseConventionalCommit(c.GetSHA(), repo.GetID(), commit.GetCommitter().GetDate().Time, commit.GetMessage())
}

// Parses a push event commit, timestamped by its timestamp, which must equal the comparison's committer date.
func fromPushCommit(repo *github.PushEventRepository, c *github.HeadCommit) (ConventionalCommit, bool) {
	return parseConventionalCommit(c.GetID(), repo.GetID(), c.GetTimestamp().Time, c.GetMessage())
}

// Builds a ConventionalCommit from a message by the rules in Parsing commit messages; ok is false when it breaks one.
func parseConventionalCommit(id string, repoID int64, timestamp time.Time, message string) (ConventionalCommit, bool) {
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	header := headerPattern.FindStringSubmatch(lines[0])
	if header == nil {
		return ConventionalCommit{}, false
	}
	c := ConventionalCommit{
		ID:          id,
		RepoID:      repoID,
		Timestamp:   timestamp,
		Type:        strings.ToLower(header[1]),
		Scope:       header[2],
		Breaking:    header[3] == "!",
		Description: header[4],
	}

	blank := func(line string) bool { return strings.TrimSpace(line) == "" }
	if len(lines) > 1 && !blank(lines[1]) {
		return ConventionalCommit{}, false // the body must begin one blank line after the description
	}

	// the lines after the header and its blank line, without leading or trailing blank lines
	rest := lines[min(2, len(lines)):]
	for len(rest) > 0 && blank(rest[0]) {
		rest = rest[1:]
	}
	for len(rest) > 0 && blank(rest[len(rest)-1]) {
		rest = rest[:len(rest)-1]
	}

	// the last paragraph is the footer block when its first line is a footer line; otherwise it belongs to the body
	start := len(rest)
	for start > 0 && !blank(rest[start-1]) {
		start--
	}
	if start < len(rest) && footerPattern.MatchString(rest[start]) {
		for _, line := range rest[start:] {
			m := footerPattern.FindStringSubmatch(line)
			if m == nil {
				// a value may span lines until the next footer line
				c.Footers[len(c.Footers)-1].Value += "\n" + line
				continue
			}
			value := m[3]
			if m[2] == " #" {
				value = "#" + value // the separator is the space, so Closes #42 has the value #42
			}
			c.Footers = append(c.Footers, Footer{Token: m[1], Value: value})
			if strings.EqualFold(m[1], "BREAKING CHANGE") || strings.EqualFold(m[1], "BREAKING-CHANGE") {
				c.Breaking = true
			}
		}
		rest = rest[:start]
	}

	for len(rest) > 0 && blank(rest[len(rest)-1]) {
		rest = rest[:len(rest)-1]
	}
	c.Body = strings.Join(rest, "\n")
	return c, true
}
