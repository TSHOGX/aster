package evolution

import "encoding/json"

// CorpusEpisode contains observable task evidence. Missing/truncated capture is
// explicit; hidden reasoning is never required or exported as a trace substitute.
type CorpusEpisode struct {
	ID        string `json:"id"`
	FamilyID  string `json:"family_id"`
	Outcome   string `json:"outcome"`
	Truncated bool   `json:"truncated"`
	Text      string `json:"text"`
}
type Corpus struct {
	Episodes []CorpusEpisode `json:"episodes"`
}

var CorpusSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["episodes"],"properties":{"episodes":{"type":"array","maxItems":1000,"items":{"type":"object","additionalProperties":false,"required":["id","family_id","outcome","truncated","text"],"properties":{"id":{"type":"string","minLength":1,"maxLength":128},"family_id":{"type":"string","minLength":1,"maxLength":128},"outcome":{"enum":["completed","failed","unknown"]},"truncated":{"type":"boolean"},"text":{"type":"string","maxLength":65536}}}}}}`)
