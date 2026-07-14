package services

import (
	"fmt"
	"strings"
	"testing"
)

// Verifies chunk boundaries and overlap with small, hand-traceable numbers:
// four 4-word segments, maxWords=10 forces a close every 2 segments, and
// each segment alone (4 words) already covers overlapWords=3, so exactly
// one trailing segment carries into the next chunk.
func TestChunkSegments_BasicOverlap(t *testing.T) {
	segments := []Segment{
		{Text: "one two three four", StartSeconds: 0, EndSeconds: 1},
		{Text: "five six seven eight", StartSeconds: 1, EndSeconds: 2},
		{Text: "nine ten eleven twelve", StartSeconds: 2, EndSeconds: 3},
		{Text: "thirteen fourteen fifteen sixteen", StartSeconds: 3, EndSeconds: 4},
	}

	chunks := ChunkSegments(segments, 10, 3)

	want := []Chunk{
		{Text: "one two three four five six seven eight", StartSeconds: 0, EndSeconds: 2, ChunkIndex: 0},
		{Text: "five six seven eight nine ten eleven twelve", StartSeconds: 1, EndSeconds: 3, ChunkIndex: 1},
		{Text: "nine ten eleven twelve thirteen fourteen fifteen sixteen", StartSeconds: 2, EndSeconds: 4, ChunkIndex: 2},
	}

	if len(chunks) != len(want) {
		t.Fatalf("expected %d chunks, got %d: %+v", len(want), len(chunks), chunks)
	}
	for i, w := range want {
		if chunks[i] != w {
			t.Errorf("chunk[%d]:\n got  %+v\n want %+v", i, chunks[i], w)
		}
	}
}

// Empty input should produce no chunks, not an error or a panic.
func TestChunkSegments_EmptyInput(t *testing.T) {
	chunks := ChunkSegments(nil, 500, 50)
	if len(chunks) != 0 {
		t.Fatalf("expected no chunks, got %d", len(chunks))
	}
}

// When the whole transcript fits under maxWords, it should collapse into a
// single chunk with no overlap logic ever triggering.
func TestChunkSegments_SingleChunkWhenUnderLimit(t *testing.T) {
	segments := []Segment{
		{Text: "one two three four", StartSeconds: 0, EndSeconds: 1},
		{Text: "five six seven eight", StartSeconds: 1, EndSeconds: 2},
	}

	chunks := ChunkSegments(segments, 500, 50)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d: %+v", len(chunks), chunks)
	}
	want := Chunk{Text: "one two three four five six seven eight", StartSeconds: 0, EndSeconds: 2, ChunkIndex: 0}
	if chunks[0] != want {
		t.Errorf("got %+v, want %+v", chunks[0], want)
	}
}

// A single segment that alone exceeds maxWords still becomes its own chunk
// (segments are never split) — and must NOT drag itself into every later
// chunk via overlap, which would otherwise cause unbounded chunk growth.
func TestChunkSegments_OversizedSegmentDoesNotLeakIntoLaterChunks(t *testing.T) {
	bigWords := make([]string, 20)
	for i := range bigWords {
		bigWords[i] = fmt.Sprintf("w%d", i)
	}
	big := Segment{Text: strings.Join(bigWords, " "), StartSeconds: 0, EndSeconds: 5}

	segments := []Segment{
		big,
		{Text: "e1 e2 e3 e4", StartSeconds: 5, EndSeconds: 6},
		{Text: "f1 f2 f3 f4", StartSeconds: 6, EndSeconds: 7},
	}

	// maxWords=10 is smaller than the 20-word oversized segment on purpose.
	chunks := ChunkSegments(segments, 10, 3)

	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].Text != big.Text || chunks[0].StartSeconds != 0 || chunks[0].EndSeconds != 5 {
		t.Errorf("chunk[0] should be the oversized segment alone, got %+v", chunks[0])
	}
	want1 := Chunk{Text: "e1 e2 e3 e4 f1 f2 f3 f4", StartSeconds: 5, EndSeconds: 7, ChunkIndex: 1}
	if chunks[1] != want1 {
		t.Errorf("chunk[1]:\n got  %+v\n want %+v", chunks[1], want1)
	}
}

// A multi-segment overlap tail whose combined word count happens to reach
// maxWords must NOT be wiped by the oversized-segment guard — that guard is
// only for a single segment that alone can never be "outgrown". Regression
// test for a bug the review process caught: a naive `bufferWords >= maxWords`
// check (without the len(buffer)==1 condition) would reset this tail too,
// silently dropping real overlap continuity between chunk[2] and chunk[3].
func TestChunkSegments_MultiSegmentOverlapTailAtThresholdIsKept(t *testing.T) {
	segments := []Segment{
		{Text: "a1 a2", StartSeconds: 0, EndSeconds: 1},
		{Text: "b1 b2", StartSeconds: 1, EndSeconds: 2},
		{Text: "c1 c2 c3 c4 c5 c6 c7 c8", StartSeconds: 2, EndSeconds: 3}, // 8 words: not oversized alone
		{Text: "d1 d2", StartSeconds: 3, EndSeconds: 4},
		{Text: "e1 e2", StartSeconds: 4, EndSeconds: 5},
	}

	chunks := ChunkSegments(segments, 10, 3)

	want := []Chunk{
		{Text: "a1 a2 b1 b2", StartSeconds: 0, EndSeconds: 2, ChunkIndex: 0},
		{Text: "a1 a2 b1 b2 c1 c2 c3 c4 c5 c6 c7 c8", StartSeconds: 0, EndSeconds: 3, ChunkIndex: 1},
		{Text: "c1 c2 c3 c4 c5 c6 c7 c8 d1 d2", StartSeconds: 2, EndSeconds: 4, ChunkIndex: 2},
		{Text: "c1 c2 c3 c4 c5 c6 c7 c8 d1 d2 e1 e2", StartSeconds: 2, EndSeconds: 5, ChunkIndex: 3},
	}

	if len(chunks) != len(want) {
		t.Fatalf("expected %d chunks, got %d: %+v", len(want), len(chunks), chunks)
	}
	for i, w := range want {
		if chunks[i] != w {
			t.Errorf("chunk[%d]:\n got  %+v\n want %+v", i, chunks[i], w)
		}
	}
}

// Sanity check against real caption text (from a live Supadata call earlier
// this session) at production-realistic 500/50 — confirms real punctuation
// and spacing survive chunking, not just clean synthetic fixtures.
func TestChunkSegments_RealCaptionData(t *testing.T) {
	segments := []Segment{
		{Text: "All right, so here we are, in front of the\nelephants", StartSeconds: 1.20, EndSeconds: 3.36},
		{Text: "the cool thing about these guys is that they\nhave really...", StartSeconds: 5.32, EndSeconds: 7.97},
		{Text: "really really long trunks", StartSeconds: 7.97, EndSeconds: 12.62},
	}

	chunks := ChunkSegments(segments, 500, 50)

	if len(chunks) != 1 {
		t.Fatalf("expected these few real segments to fit in 1 chunk, got %d", len(chunks))
	}

	want := Chunk{
		Text: "All right, so here we are, in front of the\nelephants " +
			"the cool thing about these guys is that they\nhave really... " +
			"really really long trunks",
		StartSeconds: 1.20,
		EndSeconds:   12.62,
		ChunkIndex:   0,
	}
	if chunks[0] != want {
		t.Errorf("got %+v, want %+v", chunks[0], want)
	}
}
