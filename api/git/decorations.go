package git

import "strings"

const (
	// Separator git writes between decoration entries in %D.
	decorationSeparator = ", "
	// Entry prefixes git writes in %D under --decorate=full.
	headPointerPrefix = "HEAD -> "
	tagEntryPrefix    = "tag: "
	detachedHeadEntry = "HEAD"
	// Ref namespaces. --decorate=full is what makes these present, and they are
	// the only way to tell a remote branch from a local one whose name merely
	// starts with a remote's name.
	localBranchNamespace  = "refs/heads/"
	remoteBranchNamespace = "refs/remotes/"
	tagNamespace          = "refs/tags/"

	// HeadDecorationMarker replaces git's "HEAD -> " on the checked-out branch.
	HeadDecorationMarker = "*"
	// RemoteDecorationMarker replaces the duplicate remote entry on a local branch
	// that a remote also points at. ASCII on purpose: the degree sign and middle
	// dot are East Asian Ambiguous, so a CJK-configured terminal draws them two
	// columns wide while ansi.StringWidth measures one, which misaligns the row
	// and overruns the width budget taken against that measurement.
	RemoteDecorationMarker = "^"
	// Branch and tag names cannot contain a space, so a single space delimits the
	// compacted entries unambiguously and costs a column less than ", " each.
	compactDecorationSeparator = " "
)

type DecorationKind int

const (
	LocalBranchDecoration DecorationKind = iota
	RemoteBranchDecoration
	TagDecoration
	DetachedHeadDecoration
	UnknownDecoration
)

// ------------------------------------
//
//	Decoration is one entry of a commit's %D list, classified by namespace.
//	Name holds the branch or tag name without its namespace; for a remote branch
//	Remote holds the remote it lives on, so the two halves can be recombined or
//	compared against a local branch of the same name
//
// ------------------------------------
type Decoration struct {
	Kind   DecorationKind
	Name   string
	Remote string
	// IsHead marks the branch HEAD is attached to. At most one decoration in a
	// whole log carries it.
	IsHead bool
	// OnRemote marks a local branch that at least one remote also points at for
	// this commit, which is what lets the duplicate remote entry be dropped.
	OnRemote bool
}

// ------------------------------------
//
//	Report the name git's own short decoration would have used, so a filter still
//	matches what the user is used to typing
//
// ------------------------------------
func (d Decoration) ShortName() string {
	if d.Kind == RemoteBranchDecoration {
		return d.Remote + "/" + d.Name
	}
	return d.Name
}

// ------------------------------------
//
//	Split a %D list captured under --decorate=full into classified entries, in the
//	order git wrote them. Entries are returned as they stand: no deduplication
//	happens here, because filtering wants every name the user could type while
//	only rendering wants the collapsed set
//
// ------------------------------------
func ParseDecorations(raw string) []Decoration {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	entries := strings.Split(trimmed, decorationSeparator)
	decorations := make([]Decoration, 0, len(entries))

	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		isHead := false
		if after, found := strings.CutPrefix(entry, headPointerPrefix); found {
			isHead = true
			entry = after
		}

		// git keeps the "tag: " marker even under --decorate=full, so it is
		// stripped before the namespace is read.
		entry = strings.TrimPrefix(entry, tagEntryPrefix)

		decoration := classifyDecoration(entry)
		// Assigned rather than overwritten: a bare "HEAD" entry is already marked
		// by classification, and it carries no "HEAD -> " prefix to re-derive it
		// from.
		if isHead {
			decoration.IsHead = true
		}
		decorations = append(decorations, decoration)
	}

	return decorations
}

// ------------------------------------
//
//	Classify one namespace-qualified ref
//
// ------------------------------------
func classifyDecoration(entry string) Decoration {
	switch {
	case entry == detachedHeadEntry:
		return Decoration{Kind: DetachedHeadDecoration, Name: detachedHeadEntry, IsHead: true}

	case strings.HasPrefix(entry, localBranchNamespace):
		return Decoration{Kind: LocalBranchDecoration, Name: strings.TrimPrefix(entry, localBranchNamespace)}

	case strings.HasPrefix(entry, remoteBranchNamespace):
		// The remote name is the first segment; everything after it is the branch,
		// which may itself contain slashes.
		rest := strings.TrimPrefix(entry, remoteBranchNamespace)
		remote, branch, found := strings.Cut(rest, "/")
		if !found || branch == "" {
			return Decoration{Kind: UnknownDecoration, Name: rest}
		}
		return Decoration{Kind: RemoteBranchDecoration, Name: branch, Remote: remote}

	case strings.HasPrefix(entry, tagNamespace):
		return Decoration{Kind: TagDecoration, Name: strings.TrimPrefix(entry, tagNamespace)}

	default:
		// A ref outside the three namespaces the log asks for. Shown as written
		// rather than dropped, so an unexpected decoration is visible instead of
		// silently missing.
		return Decoration{Kind: UnknownDecoration, Name: entry}
	}
}

// ------------------------------------
//
//	Collapse a commit's decorations into the shortest form that keeps their
//	meaning. git names a pushed branch twice — once local, once per remote — and
//	that repetition, not the "HEAD -> " and "origin/" labels, is what makes the
//	raw list too wide for a commit log row. A local branch a remote also points at
//	is written once with a trailing marker; the checked-out branch takes a leading
//	marker in place of "HEAD -> "; a tag drops its namespace and is told apart by
//	colour. Only a branch that exists on a remote with no local counterpart keeps
//	its remote prefix, because there the remote name is the information
//
// ------------------------------------
func CompactDecorations(raw string) string {
	decorations := ParseDecorations(raw)
	if len(decorations) == 0 {
		return ""
	}

	// A local branch can be matched by several remotes at once. The marker means
	// "at least one remote is here too", so the first match settles it.
	localByName := make(map[string]int, len(decorations))
	for index, decoration := range decorations {
		if decoration.Kind == LocalBranchDecoration {
			localByName[decoration.Name] = index
		}
	}

	dropped := make([]bool, len(decorations))
	for index, decoration := range decorations {
		if decoration.Kind != RemoteBranchDecoration {
			continue
		}
		if localIndex, found := localByName[decoration.Name]; found {
			decorations[localIndex].OnRemote = true
			dropped[index] = true
		}
	}

	// Rendered in a fixed order rather than git's, so a row reads the same way
	// regardless of the order the refs happened to be written in.
	var head, locals, remotes, tags, others []string
	for index, decoration := range decorations {
		if dropped[index] {
			continue
		}

		text := compactDecorationText(decoration)
		switch {
		case decoration.IsHead:
			head = append(head, text)
		case decoration.Kind == LocalBranchDecoration:
			locals = append(locals, text)
		case decoration.Kind == RemoteBranchDecoration:
			remotes = append(remotes, text)
		case decoration.Kind == TagDecoration:
			tags = append(tags, text)
		default:
			others = append(others, text)
		}
	}

	ordered := make([]string, 0, len(decorations))
	ordered = append(ordered, head...)
	ordered = append(ordered, locals...)
	ordered = append(ordered, remotes...)
	ordered = append(ordered, tags...)
	ordered = append(ordered, others...)

	return strings.Join(ordered, compactDecorationSeparator)
}

// ------------------------------------
//
//	Render one decoration in its compact form
//
// ------------------------------------
func compactDecorationText(decoration Decoration) string {
	if decoration.Kind == DetachedHeadDecoration {
		// The row already carries the commit hash in its first column, so the
		// marker only has to say that this is where HEAD sits.
		return HeadDecorationMarker + detachedHeadEntry
	}

	var builder strings.Builder
	if decoration.IsHead {
		builder.WriteString(HeadDecorationMarker)
	}
	builder.WriteString(decoration.ShortName())
	if decoration.OnRemote {
		builder.WriteString(RemoteDecorationMarker)
	}
	return builder.String()
}

// ------------------------------------
//
//	Build the text a commit's refs contribute to list filtering. Every name is
//	offered in the short form git would have printed, including the remote
//	duplicates the compact rendering drops, so filtering by either "main" or
//	"origin/main" still finds the commit. The namespace prefixes are deliberately
//	absent: leaving them in would make "heads" match every decorated commit
//
// ------------------------------------
func DecorationFilterText(raw string) string {
	decorations := ParseDecorations(raw)
	if len(decorations) == 0 {
		return ""
	}

	names := make([]string, 0, len(decorations))
	for _, decoration := range decorations {
		if decoration.Kind == DetachedHeadDecoration {
			continue
		}
		names = append(names, decoration.ShortName())
	}

	return strings.Join(names, compactDecorationSeparator)
}

// ------------------------------------
//
//	Name the branch a commit belongs to, for callers that need one branch rather
//	than the whole decoration list. The checked-out branch wins, then any other
//	local branch, then a remote-only one. A commit decorated solely by tags has no
//	branch to name and returns empty, which is truthful where printing the tag
//	under a "from branch" label would not be
//
// ------------------------------------
func DecorationBranchLabel(raw string) string {
	decorations := ParseDecorations(raw)

	for _, decoration := range decorations {
		if decoration.IsHead && decoration.Kind == LocalBranchDecoration {
			return decoration.Name
		}
	}
	for _, decoration := range decorations {
		if decoration.Kind == LocalBranchDecoration {
			return decoration.Name
		}
	}
	for _, decoration := range decorations {
		if decoration.Kind == RemoteBranchDecoration {
			return decoration.ShortName()
		}
	}

	return ""
}
