package realdata

import (
	"bytes"
	"fmt"
	"path"
	"strings"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The converters of datasets that ship JSON. Each JSON file (or JSON Lines line) is an item for strict_json
// as it is, and the text fields inside become text items as they are. A file strict_json refuses gives its
// JSON item and no text items, and is counted.

// strictValue reads a JSON value by strict_json's rules; ok is false when they refuse it.
func strictValue(data []byte) (any, bool) {
	value, err := strictjson.Loads(data)
	return value, err == nil
}

func field(value any, key string) (any, bool) {
	o, ok := value.(*strictjson.Object)
	if !ok {
		return nil, false
	}
	return o.Get(key)
}

func text(value any, key string) (string, bool) {
	v, ok := field(value, key)
	s, isString := v.(string)
	return s, ok && isString
}

// NOTSOFAR-1: each meeting folder holds gt_transcription.json, a list of utterance records with "text", and
// gt_meeting_metadata.json, whose "Hashtags" name #NaturalMeeting for the natural meetings.
func convertNOTSOFAR(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"meetings": 0, "natural meetings": 0, "utterances": 0, "utterances without text": 0,
		"files strict_json refuses": 0}
	kind := map[string]string{}
	err := files.Walk("benchmark-datasets/*/*/MTG/*/gt_meeting_metadata.json", func(m Member) error {
		data, err := m.Read()
		if err != nil {
			return err
		}
		if err := addJSON(add, m.Inner, m.Inner, "", data); err != nil {
			return err
		}
		value, ok := strictValue(data)
		if !ok {
			counts["files strict_json refuses"]++
		}
		tags, _ := text(value, "Hashtags")
		kind[path.Dir(m.Inner)] = "role-played"
		if strings.Contains(tags, "#NaturalMeeting") {
			kind[path.Dir(m.Inner)] = "natural"
			counts["natural meetings"]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = files.Walk("benchmark-datasets/*/*/MTG/*/gt_transcription.json", func(m Member) error {
		counts["meetings"]++
		data, err := m.Read()
		if err != nil {
			return err
		}
		meeting := kind[path.Dir(m.Inner)]
		if meeting == "" {
			return fmt.Errorf("%s has no gt_meeting_metadata.json beside it", m.Inner)
		}
		if err := addJSON(add, m.Inner, m.Inner, meeting, data); err != nil {
			return err
		}
		value, ok := strictValue(data)
		records, isList := value.([]any)
		if !ok || !isList {
			counts["files strict_json refuses"]++
			return nil
		}
		for i, record := range records {
			counts["utterances"]++
			words, ok := text(record, "text")
			if !ok {
				counts["utterances without text"]++
				continue
			}
			if err := add(Item{ID: fmt.Sprintf("%s#%d", m.Inner, i), File: m.Inner, Kind: meeting, Text: words}); err != nil {
				return err
			}
		}
		return nil
	})
	return counts, err
}

// AppTek: each line of diarization/*/metadata.jsonl is one call, with "segments" that each carry "text".
func convertAppTek(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"calls": 0, "segments": 0, "segments without text": 0, "lines strict_json refuses": 0}
	err := files.Walk("diarization/*/metadata.jsonl", func(m Member) error {
		data, err := m.Read()
		if err != nil {
			return err
		}
		for n, line := range bytes.Split(data, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			counts["calls"]++
			id := fmt.Sprintf("%s:%d", m.Inner, n+1)
			if err := addJSON(add, id, m.Inner, "", line); err != nil {
				return err
			}
			value, ok := strictValue(line)
			segments, _ := field(value, "segments")
			list, isList := segments.([]any)
			if !ok || !isList {
				counts["lines strict_json refuses"]++
				continue
			}
			for i, segment := range list {
				counts["segments"]++
				words, ok := text(segment, "text")
				if !ok {
					counts["segments without text"]++
					continue
				}
				if err := add(Item{ID: fmt.Sprintf("%s#%d", id, i), File: m.Inner, Text: words}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return counts, err
}

// A Slack export: raw/channels.json, users.json and integration_logs.json, and raw/CHANNEL/DAY.json, a list of
// messages. Every file is a JSON item; each message's "text" is a text item, its markup kept. safe_text does
// not decode &amp;, &lt; and &gt;, so the messages that hold them are counted.
func convertSlackExport(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"files": 0, "day files": 0, "messages": 0, "messages without text": 0,
		"messages with &amp; &lt; or &gt;": 0, "files strict_json refuses": 0}
	visit := func(day bool) func(Member) error {
		return func(m Member) error {
			counts["files"]++
			data, err := m.Read()
			if err != nil {
				return err
			}
			if err := addJSON(add, m.Inner, m.Inner, "", data); err != nil {
				return err
			}
			value, ok := strictValue(data)
			if !ok {
				counts["files strict_json refuses"]++
				return nil
			}
			if !day {
				return nil
			}
			counts["day files"]++
			messages, _ := value.([]any)
			for i, message := range messages {
				counts["messages"]++
				words, ok := text(message, "text")
				if !ok {
					counts["messages without text"]++
					continue
				}
				if strings.Contains(words, "&amp;") || strings.Contains(words, "&lt;") || strings.Contains(words, "&gt;") {
					counts["messages with &amp; &lt; or &gt;"]++
				}
				if err := add(Item{ID: fmt.Sprintf("%s#%d", m.Inner, i), File: m.Inner, Text: words}); err != nil {
					return err
				}
			}
			return nil
		}
	}
	// The archive holds one folder named for the repository and commit.
	if err := files.Walk("*/raw/*.json", visit(false)); err != nil {
		return nil, err
	}
	return counts, files.Walk("*/raw/*/*.json", visit(true))
}

// An sObject tree (sf data tree import): data/data-plan.json names the files, each {"records": [...]}. Every
// file is a JSON item. Each record's name fields (a key that ends in "name", in any case), title and
// description are text items.
func convertSObjectTree(files *Files, add func(Item) error) (map[string]int, error) {
	counts := map[string]int{"files": 0, "records": 0, "fields": 0, "files strict_json refuses": 0}
	err := files.Walk("data/*.json", func(m Member) error {
		counts["files"]++
		data, err := m.Read()
		if err != nil {
			return err
		}
		if err := addJSON(add, m.Inner, m.Inner, "", data); err != nil {
			return err
		}
		value, ok := strictValue(data)
		if !ok {
			counts["files strict_json refuses"]++
			return nil
		}
		records, _ := field(value, "records")
		list, _ := records.([]any)
		for i, record := range list {
			counts["records"]++
			o, _ := record.(*strictjson.Object)
			if o == nil {
				continue
			}
			for j, key := range o.Keys {
				lower := strings.ToLower(key)
				words, isString := o.Values[j].(string)
				if !isString || !(strings.HasSuffix(lower, "name") || lower == "title" || lower == "description") {
					continue
				}
				counts["fields"]++
				if err := add(Item{ID: fmt.Sprintf("%s#%d.%s", m.Inner, i, key), File: m.Inner, Text: words}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return counts, err
}
