package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
)

// File is gtm.yaml as read: the settings this build applies, and the ones it knows and does not apply yet.
type File struct {
	// Sources are the sources the file names, in the file's order.
	Sources []Source
	// NotApplied are the settings the file holds that this build does not apply, as dotted paths in the
	// file's order, each once. A file with any cannot be activated.
	NotApplied []string
}

// Source is one entry of sources: the engineer's own name for it, and its kind.
type Source struct {
	Name string
	// Kind is salesforce, demo, slack or transcripts.
	Kind string
	Line int
}

// The kinds of source a file may name. A kind this build reads is connected with connect; the others are
// in the guide and not built.
var (
	builtKinds   = map[string]bool{"salesforce": true, "demo": true, "slack": true, "transcripts": true}
	plannedKinds = map[string]bool{"gong": true, "push": true, "sql": true}
	// zoneName is the form of a time zone's name: UTC, or an area and a place, as America/Chicago.
	zoneName = regexp.MustCompile(`^(UTC|[A-Z][A-Za-z_]+(/[A-Za-z0-9_+-]+){1,2})$`)
)

// Revision is the name of a file's exact bytes: their SHA-256 in lower-case hex. The same bytes are the
// same revision on any machine.
func Revision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Read reads gtm.yaml. A file that is not the YAML the reader takes, that holds a key the guide does not
// give, or a value of the wrong kind, is refused with its line.
func Read(data []byte) (*File, error) {
	root, err := parse(data)
	if err != nil {
		return nil, err
	}
	file := &File{}
	later := func(path string) {
		for _, held := range file.NotApplied {
			if held == path {
				return
			}
		}
		file.NotApplied = append(file.NotApplied, path)
	}
	for _, key := range root.keys {
		value := root.items[key]
		switch key {
		case "sources":
			if !value.mapping() {
				return nil, refuse(value.line, "sources is a mapping of names to sources, not %s", value.kind())
			}
			for _, name := range value.keys {
				source, err := readSource(name, value.items[name], later)
				if err != nil {
					return nil, err
				}
				if source != nil {
					file.Sources = append(file.Sources, *source)
				}
			}
		case "approval":
			if value.text == nil || (*value.text != "ambiguous" && *value.text != "all") {
				return nil, refuse(value.line, "approval is ambiguous or all")
			}
			later("approval")
		case "watch":
			if err := numbers(value, "watch", map[string]bool{"renewals_closing_within_days": true}); err != nil {
				return nil, err
			}
			later("watch")
		case "rules":
			if err := numbers(value, "rules", map[string]bool{"customer_silence_days": true}); err != nil {
				return nil, err
			}
			later("rules")
		case "timezone":
			// The form of a zone's name is checked here. Whether the zone exists is checked by the build that
			// applies it.
			if value.text == nil || !zoneName.MatchString(*value.text) {
				return nil, refuse(value.line, "timezone is a zone's name, such as America/Chicago")
			}
			later("timezone")
		default:
			return nil, refuse(value.line, "this is no setting of gtm.yaml; the settings are approval, rules, sources, timezone and watch")
		}
	}
	return file, nil
}

// readSource reads one source. A source of a kind no build reads yet is not kept: it is a setting that is
// not applied.
func readSource(name string, value *node, later func(string)) (*Source, error) {
	if !value.mapping() {
		return nil, refuse(value.line, "a source is a mapping with its kind, as {kind: slack}")
	}
	kind, given := value.items["kind"]
	if !given || kind.text == nil {
		return nil, refuse(value.line, "the source has no kind")
	}
	for _, key := range value.keys {
		entry := value.items[key]
		switch {
		case key == "kind":
		case key == "approval":
			if entry.text == nil || (*entry.text != "ambiguous" && *entry.text != "all") {
				return nil, refuse(entry.line, "approval is ambiguous or all")
			}
			later("sources." + name + ".approval")
		case key == "channels":
			if entry.list == nil {
				return nil, refuse(entry.line, "channels is a list of channel names, as [\"#deal-*\", \"#renewals\"]")
			}
			for _, channel := range entry.list {
				if channel.text == nil || *channel.text == "" {
					return nil, refuse(entry.line, "channels is a list of channel names, as [\"#deal-*\", \"#renewals\"]")
				}
			}
			later("sources." + name + ".channels")
		case key == "folder" || key == "view":
			if entry.text == nil || *entry.text == "" {
				return nil, refuse(entry.line, "%s is a name, as text", key)
			}
			later("sources." + name + "." + key)
		default:
			return nil, refuse(entry.line, "this is no setting of a source; a source has kind, approval, channels, folder and view")
		}
	}
	switch {
	case builtKinds[*kind.text]:
		return &Source{Name: name, Kind: *kind.text, Line: value.line}, nil
	case plannedKinds[*kind.text]:
		later("sources." + name)
		return nil, nil
	}
	return nil, refuse(kind.line, "this is no kind of source; the kinds are demo, salesforce, slack, transcripts, gong, push and sql")
}

// numbers checks a mapping whose known keys each hold a whole number above zero.
func numbers(value *node, name string, known map[string]bool) error {
	if !value.mapping() {
		return refuse(value.line, "%s is a mapping, not %s", name, value.kind())
	}
	for _, key := range value.keys {
		entry := value.items[key]
		if !known[key] {
			return refuse(entry.line, "this is no setting of %s", name)
		}
		if entry.whole == nil || *entry.whole < 1 {
			return refuse(entry.line, "%s.%s is a whole number of days, 1 or more", name, key)
		}
	}
	return nil
}

// Settings are the settings this build applies, as the JSON the database keeps beside the file's bytes:
// {"sources": {NAME: {"kind": KIND}, ...}}, with its keys in order.
func (f *File) Settings() []byte {
	sources := map[string]map[string]string{}
	for _, source := range f.Sources {
		sources[source.Name] = map[string]string{"kind": source.Kind}
	}
	encoded, _ := json.Marshal(map[string]any{"sources": sources})
	return encoded
}
