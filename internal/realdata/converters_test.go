package realdata

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

// Each converter runs on a small invented sample in its dataset's layout.

func buildTarGz(t *testing.T, names []string, files map[string]string) []byte {
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	w := tar.NewWriter(zipped)
	for _, name := range names {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(files[name])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(files[name]))
	}
	if err := w.WriteHeader(&tar.Header{Name: "top/link.json", Linkname: "/etc/hosts", Typeflag: tar.TypeSymlink}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	zipped.Close()
	return buffer.Bytes()
}

// convertSample converts the downloads and gives the items, without their line numbers, and the counts.
func convertSample(t *testing.T, converter string, downloads map[string][]byte) ([]Item, map[string]int) {
	t.Helper()
	cache, d := placeFiles(t, converter, downloads)
	summary, err := Convert(cache, d)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := readItems(t, DatasetRun(cache, d).Items, false)
	for i := range items {
		items[i].Line = 0
	}
	return items, summary.Counts
}

func textItems(items []Item) map[string]string {
	found := map[string]string{}
	for _, it := range items {
		if !it.IsJSON {
			found[it.ID] = it.Kind + "|" + it.Text
		}
	}
	return found
}

func jsonItems(items []Item) []string {
	var found []string
	for _, it := range items {
		if it.IsJSON {
			found = append(found, it.ID)
		}
	}
	return found
}

func TestConvertNOTSOFAR(t *testing.T) {
	base := "benchmark-datasets/dev_set/240825.1_dev1/MTG/"
	items, counts := convertSample(t, "notsofar", map[string][]byte{
		base + "MTG_1/gt_meeting_metadata.json": []byte(`{"meeting_id": "MTG_1", "Hashtags": "#NaturalMeeting #Office"}`),
		base + "MTG_1/gt_transcription.json":    []byte(`[{"speaker_id": "P1", "text": "We agree.", "start_time": 0.5}, {"speaker_id": "P2", "text": "Yes"}]`),
		base + "MTG_2/gt_meeting_metadata.json": []byte(`{"meeting_id": "MTG_2", "Hashtags": "#Debate"}`),
		base + "MTG_2/gt_transcription.json":    []byte(`[{"speaker_id": "P1", "start_time": 1}, {"text": "Fine."}]`),
	})
	want := map[string]string{
		base + "MTG_1/gt_transcription.json#0": "natural|We agree.",
		base + "MTG_1/gt_transcription.json#1": "natural|Yes",
		base + "MTG_2/gt_transcription.json#1": "role-played|Fine.",
	}
	if got := textItems(items); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if len(jsonItems(items)) != 4 || counts["natural meetings"] != 1 || counts["utterances without text"] != 1 {
		t.Errorf("%v %v", jsonItems(items), counts)
	}
}

func TestConvertAppTek(t *testing.T) {
	lines := `{"file_name": "a.wav", "segments": [{"role": "agent", "speaker_id": "A1", "text": "How can I help?"}, {"role": "customer", "text": "My bill."}]}` +
		"\n\n" + `{"file_name": "b.wav", "segments": [{"text": "Hi"}], "x": 1, "x": 2}` + "\n"
	items, counts := convertSample(t, "apptek-jsonl", map[string][]byte{"diarization/en-XX/metadata.jsonl": []byte(lines)})
	want := map[string]string{
		"diarization/en-XX/metadata.jsonl:1#0": "role-played|How can I help?",
		"diarization/en-XX/metadata.jsonl:1#1": "role-played|My bill.",
	}
	if got := textItems(items); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	// The line strict_json refuses (a duplicate key) is still compared, as a document.
	if got := jsonItems(items); !reflect.DeepEqual(got, []string{"diarization/en-XX/metadata.jsonl:1", "diarization/en-XX/metadata.jsonl:3"}) ||
		counts["lines strict_json refuses"] != 1 {
		t.Errorf("%v %v", got, counts)
	}
}

var slackFiles = map[string]string{
	"top/README.md":         "not read",
	"top/json/general.json": `[{"text": "another export, not read"}]`,
	"top/raw/channels.json": `[{"id": "C1", "name": "general"}]`,
	"top/raw/users.json":    `[{"id": "U1", "name": "someone"}]`,
	"top/raw/general/2016-01-07.json": `[{"type": "message", "user": "U1", "text": "<@U1> see <https://a.example/x|the doc> &amp; more", "ts": "1.0"},` +
		`{"type": "message", "subtype": "channel_join", "text": "<@U2|someone> has joined the channel"}, {"type": "message", "text": null}]`,
}

func TestConvertSlackExport(t *testing.T) {
	order := []string{"top/README.md", "top/json/general.json", "top/raw/channels.json", "top/raw/users.json", "top/raw/general/2016-01-07.json"}
	items, counts := convertSample(t, "slack-export", map[string][]byte{"commit.tar.gz": buildTarGz(t, order, slackFiles)})
	want := map[string]string{
		"top/raw/general/2016-01-07.json#0": "role-played|" + "<@U1> see <https://a.example/x|the doc> &amp; more",
		"top/raw/general/2016-01-07.json#1": "role-played|<@U2|someone> has joined the channel",
	}
	if got := textItems(items); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if got := jsonItems(items); !reflect.DeepEqual(got, []string{"top/raw/channels.json", "top/raw/users.json", "top/raw/general/2016-01-07.json"}) {
		t.Errorf("%v", got)
	}
	if counts["messages without text"] != 1 || counts["messages with &amp; &lt; or &gt;"] != 1 {
		t.Errorf("%v", counts)
	}
}

func TestConvertRevNLP(t *testing.T) {
	earnings21 := "token|speaker|ts|endTs|punctuation|case|tags|wer_tags\n" +
		"good|1|||,|UC|[]|[]\nmorning|1|||.|LC|[]|[]\nrevenue|1|||.|LC|['0:MONEY']|[]\n" +
		"thanks|2|||!|UC|[]|[]\nusa|2|||.|CA|[]|[]\nq|1|||?|UC|[]|[]\n"
	earnings22 := "token|speaker|ts|endTs|punctuation|prepunctuation|case|tags\n" +
		"hello|1|||,||UC|[]\nquote|1|||\"|\"|LC|[]\n"
	items, counts := convertSample(t, "rev-nlp", map[string][]byte{
		"earnings21/transcripts/nlp_references/1.nlp":    []byte(earnings21),
		"earnings21/transcripts/wer_tags/1.wer_tag.json": []byte(`{"0": {"entity_type": "MONEY"}}`),
		"earnings22/transcripts/nlp_references/2.nlp":    []byte(earnings22),
	})
	want := map[string]string{
		"earnings21/transcripts/nlp_references/1.nlp#1": "role-played|Good, morning. revenue.",
		"earnings21/transcripts/nlp_references/1.nlp#2": "role-played|Thanks! USA.",
		"earnings21/transcripts/nlp_references/1.nlp#3": "role-played|Q?",
		"earnings22/transcripts/nlp_references/2.nlp#1": "role-played|Hello, \"quote\"",
	}
	if got := textItems(items); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if len(jsonItems(items)) != 1 || counts["tokens"] != 8 || counts["turns"] != 4 {
		t.Errorf("%v %v", jsonItems(items), counts)
	}
}

func TestConvertIRC(t *testing.T) {
	log := "[03:56] <alice> hi all, see http://a.example/x\n" +
		"=== bob [~b@host] has joined #channel\n" +
		"[03:57] <bob> \x02bold\x02 \xff\n" +
		"[03:58] <carol> \r\n"
	items, counts := convertSample(t, "irc-raw", map[string][]byte{"data/train/2004-11-15.train-a.raw.txt": []byte(log)})
	want := map[string]string{
		"data/train/2004-11-15.train-a.raw.txt:1": "role-played|hi all, see http://a.example/x",
		"data/train/2004-11-15.train-a.raw.txt:4": "role-played|",
	}
	if got := textItems(items); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if counts["other lines"] != 1 || counts["lines that are not UTF-8"] != 1 {
		t.Errorf("%v", counts)
	}
}

func TestConvertCSV(t *testing.T) {
	archive := buildZip(t, map[string]string{
		"pythondev.csv":           "\ufeffthread,time,speaker,message\n1,0,u1,\"Hello, \"\"team\"\"\nnew line\"\n1,5,u2,<@U1> ok\n",
		"archival_annotation.csv": "conversation_id,archival,community\n1,yes,python\n",
	})
	items, counts := convertSample(t, "csv-columns", map[string][]byte{"chat.zip": archive})
	want := map[string]string{"pythondev.csv:2": "role-played|Hello, \"team\"\nnew line", "pythondev.csv:3": "role-played|<@U1> ok"}
	if got := textItems(items); !reflect.DeepEqual(got, want) || counts["files without a message column"] != 1 {
		t.Errorf("%v %v", got, counts)
	}
	seeds := "id,name,amount\n001,Acme Ltd.,\n002,,12.5\n"
	items, counts = convertSample(t, "csv-cells", map[string][]byte{"integration_tests/seeds/sf_account_data.csv": []byte(seeds)})
	want = map[string]string{
		"integration_tests/seeds/sf_account_data.csv:2:1": "role-played|001",
		"integration_tests/seeds/sf_account_data.csv:2:2": "role-played|Acme Ltd.",
		"integration_tests/seeds/sf_account_data.csv:3:1": "role-played|002",
		"integration_tests/seeds/sf_account_data.csv:3:3": "role-played|12.5",
	}
	if got := textItems(items); !reflect.DeepEqual(got, want) || counts["empty cells"] != 2 {
		t.Errorf("%v %v", got, counts)
	}
	cache, d := placeFiles(t, "csv-cells", map[string][]byte{"x/seeds/a.csv": []byte("a,b\n\"open,1\n")})
	if _, err := Convert(cache, d); err == nil {
		t.Error("broken CSV converted")
	}
}

func TestConvertSObjectTree(t *testing.T) {
	items, _ := convertSample(t, "sobject-tree", map[string][]byte{
		"data/data-plan.json": []byte(`[{"sobject": "Account", "files": ["Accounts.json"]}]`),
		"data/Accounts.json": []byte(`{"records": [{"attributes": {"type": "Account"}, "Name": "Acme ‮ Ltd", "Phone": "1",` +
			` "description": "Makes things."}, {"firstName": "Ada", "LastName": "Lovelace", "Title": "CTO", "email": "a@b.example"}]}`),
	})
	want := map[string]string{
		"data/Accounts.json#0.Name":        "role-played|Acme ‮ Ltd",
		"data/Accounts.json#0.description": "role-played|Makes things.",
		"data/Accounts.json#1.firstName":   "role-played|Ada",
		"data/Accounts.json#1.LastName":    "role-played|Lovelace",
		"data/Accounts.json#1.Title":       "role-played|CTO",
	}
	if got := textItems(items); !reflect.DeepEqual(got, want) || len(jsonItems(items)) != 2 {
		t.Errorf("%v %v", got, jsonItems(items))
	}
}

func utf16BE(text string) string {
	var b bytes.Buffer
	b.Write([]byte{0xFE, 0xFF})
	for _, unit := range utf16.Encode([]rune(text)) {
		binary.Write(&b, binary.BigEndian, unit)
	}
	return b.String()
}

const sampleTextGrid = `File type = "ooTextFile"
Object class = "TextGrid"

xmin = 0
xmax = 9
tiers? <exists>
size = 1
item []:
    item [1]:
        class = "IntervalTier"
        name = "S1"
        intervals: size = 3
        intervals [1]:
            xmin = 0
            xmax = 1
            text = ""
        intervals [2]:
            xmin = 1
            xmax = 5
            text = "我们同意。他说""好""。<sil>"
        intervals [3]:
            xmin = 5
            xmax = 9
            text = "明天签字！"
`

func TestConvertTextGrids(t *testing.T) {
	order := []string{"test/TextGrid/S01.TextGrid", "test/TextGrid/S02.TextGrid", "test/wav/S01.flac"}
	archive := buildTarGz(t, order, map[string]string{
		"test/TextGrid/S01.TextGrid": sampleTextGrid,
		"test/TextGrid/S02.TextGrid": utf16BE(sampleTextGrid),
		"test/wav/S01.flac":          "audio",
	})
	items, counts := convertSample(t, "textgrid", map[string][]byte{"test.tar.gz": archive})
	want := map[string]string{
		"test/TextGrid/S01.TextGrid#2": "role-played|我们同意。他说\"好\"。<sil>",
		"test/TextGrid/S01.TextGrid#3": "role-played|明天签字！",
		"test/TextGrid/S02.TextGrid#2": "role-played|我们同意。他说\"好\"。<sil>",
		"test/TextGrid/S02.TextGrid#3": "role-played|明天签字！",
	}
	if got := textItems(items); !reflect.DeepEqual(got, want) || counts["empty intervals"] != 2 {
		t.Errorf("%v %v", got, counts)
	}
	lone := string([]byte{0xFE, 0xFF, 0xD8, 0x00, 0x00, 0x41})
	if _, ok := decodeTextGrid([]byte(lone)); ok {
		t.Error("a lone surrogate was decoded")
	}
	if text, ok := decodeTextGrid([]byte(utf16BE("a\U0001F642"))); !ok || text != "a\U0001F642" || strings.ContainsRune(text, 0xFFFD) {
		t.Errorf("a surrogate pair: %q %v", text, ok)
	}
}
