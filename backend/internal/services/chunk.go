package services

import "strings"

// Chunk is a merged run of transcript segments, sized for embedding — large
// enough to carry context, small enough to stay specific. Its time range
// always comes from real segment boundaries, never an interpolated guess.
type Chunk struct {
	Text         string
	StartSeconds float64
	EndSeconds   float64
	ChunkIndex   int // 0-based
}

// ChunkSegments merges consecutive segments into ~maxWords-sized chunks with
// ~overlapWords of repeated trailing content between consecutive chunks.
// Segments are never split — a chunk boundary always falls on a real segment
// edge, so timestamps never need to be interpolated mid-caption.
func ChunkSegments(segments []Segment, maxWords, overlapWords int) []Chunk {
	if len(segments) == 0 {
		return nil
	}

	var chunks []Chunk
	var buffer []Segment
	bufferWords := 0

	for _, seg := range segments {
		segWords := len(strings.Fields(seg.Text))

		// Adding this segment would push the current chunk past maxWords —
		// close it now rather than let it grow unbounded. Only close if
		// something's actually buffered; an empty chunk is never valid.
		if len(buffer) > 0 && bufferWords+segWords > maxWords {
			chunks = append(chunks, newChunk(buffer, len(chunks)))

			// Start the next chunk with overlap: carry forward trailing
			// segments from the chunk we just closed until they cover
			// ~overlapWords, so content near the boundary isn't lost to
			// either side.
			buffer, bufferWords = trailingOverlap(buffer, overlapWords)

			// Guard against a single oversized segment (itself >= maxWords)
			// dragging itself into every future chunk via overlap: since it
			// alone always satisfies overlapWords, trailingOverlap would keep
			// re-selecting just that one segment forever, growing unbounded.
			// Scoped to len(buffer)==1 specifically — an overlap tail made of
			// several ordinary segments that merely sums to >= maxWords is
			// harmless (it won't keep re-selecting itself the same way) and
			// resetting it would wrongly throw away real overlap continuity.
			if len(buffer) == 1 && bufferWords >= maxWords {
				buffer, bufferWords = nil, 0
			}
		}

		buffer = append(buffer, seg)
		bufferWords += segWords
	}

	// Flush whatever's left as the final chunk.
	if len(buffer) > 0 {
		chunks = append(chunks, newChunk(buffer, len(chunks)))
	}

	return chunks
}

// newChunk builds a Chunk from a run of segments: joined text, and a time
// range spanning the first segment's start to the last segment's end.
func newChunk(segs []Segment, index int) Chunk {
	texts := make([]string, len(segs))
	for i, s := range segs {
		texts[i] = s.Text
	}
	return Chunk{
		Text:         strings.Join(texts, " "),
		StartSeconds: segs[0].StartSeconds,
		EndSeconds:   segs[len(segs)-1].EndSeconds,
		ChunkIndex:   index,
	}
}

// trailingOverlap returns the tail of segs (in original order) whose
// combined word count is at least overlapWords, plus that word count. It
// walks backward a whole segment at a time — segments are never split — so
// it may overshoot overlapWords slightly, or return everything if the whole
// chunk is smaller than overlapWords.
func trailingOverlap(segs []Segment, overlapWords int) ([]Segment, int) {
	if overlapWords <= 0 {
		return nil, 0
	}

	words := 0
	start := len(segs)
	for start > 0 {
		words += len(strings.Fields(segs[start-1].Text))
		start--
		if words >= overlapWords {
			break
		}
	}

	// Copy rather than reslice so the returned tail doesn't alias segs'
	// backing array — the caller appends to it next.
	tail := append([]Segment(nil), segs[start:]...)
	return tail, words
}
