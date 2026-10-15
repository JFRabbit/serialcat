package nodes

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JFRabbit/serialcat/pkg/dag"
)

func TestBatchToFrameSlice_Empty(t *testing.T) {
	frames := batchToFrameSlice(nil)
	if len(frames) != 0 {
		t.Fatalf("nil batch should return empty slice, got %d", len(frames))
	}
	frames = batchToFrameSlice([]dag.Item{})
	if len(frames) != 0 {
		t.Fatalf("empty batch should return empty slice, got %d", len(frames))
	}
	// An empty slice should serialize to []
	if b, err := json.Marshal(frames); err != nil || string(b) != "[]" {
		t.Fatalf("empty slice serialized = %s, err=%v, want []", b, err)
	}
}

func TestBatchToFrameSlice_MultiItems(t *testing.T) {
	ts1 := time.Date(2026, 9, 30, 10, 0, 0, 123456789, time.UTC)
	ts2 := ts1.Add(time.Second)
	batch := []dag.Item{
		{Ts: ts1, Text: "line-1"},
		{Ts: ts2, Text: "line-2"},
	}
	frames := batchToFrameSlice(batch)
	if len(frames) != 2 {
		t.Fatalf("len = %d, want 2", len(frames))
	}
	if frames[0].Ts != ts1 || frames[0].Text != "line-1" {
		t.Fatalf("frames[0] = %+v", frames[0])
	}
	if frames[1].Ts != ts2 || frames[1].Text != "line-2" {
		t.Fatalf("frames[1] = %+v", frames[1])
	}
	// frame does not carry Meta/Src, so these keys must not appear in the JSON after serialization
	b, err := json.Marshal(frames)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for i, m := range raw {
		if _, ok := m["meta"]; ok {
			t.Fatalf("frames[%d] should not contain meta key: %s", i, b)
		}
		if _, ok := m["src"]; ok {
			t.Fatalf("frames[%d] should not contain src key: %s", i, b)
		}
	}
}

// Round trip: Item → frame → JSON → frame → Item, with Ts/Text kept consistent.
func TestFrame_RoundTrip(t *testing.T) {
	ts := time.Date(2026, 9, 30, 12, 34, 56, 0, time.FixedZone("CST", 8*3600))
	batch := []dag.Item{
		{Ts: ts, Text: "hello"},
		{Ts: ts.Add(time.Millisecond), Text: ""},
	}
	frames := batchToFrameSlice(batch)
	b, err := json.Marshal(frames)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back []frame
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back) != len(batch) {
		t.Fatalf("len = %d, want %d", len(back), len(batch))
	}
	for i, f := range back {
		it := f.item()
		if !it.Ts.Equal(batch[i].Ts) {
			t.Fatalf("item[%d].Ts = %v, want %v (note timezone serialization)", i, it.Ts, batch[i].Ts)
		}
		if it.Text != batch[i].Text {
			t.Fatalf("item[%d].Text = %q, want %q", i, it.Text, batch[i].Text)
		}
	}
}

// frame.item() direct conversion: fields map one to one.
func TestFrame_Item(t *testing.T) {
	ts := time.Now()
	f := frame{Ts: ts, Text: "x"}
	it := f.item()
	if !it.Ts.Equal(ts) || it.Text != "x" {
		t.Fatalf("item() = %+v", it)
	}
	// zero frame → zero Item
	zero := frame{}.item()
	if !zero.Ts.IsZero() || zero.Text != "" || zero.Meta != nil || zero.Src != "" {
		t.Fatalf("zero frame should convert to zero Item, got %+v", zero)
	}
}
