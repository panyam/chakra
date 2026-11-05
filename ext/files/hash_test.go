package files

import "testing"

func TestHashIsStableAndSensitive(t *testing.T) {
	a := Hash("one line\n")
	if a != Hash("one line\n") {
		t.Error("Hash is not stable across calls")
	}
	if a == Hash("one line") {
		t.Error("Hash ignored a trailing newline, which is a real difference on disk")
	}
	if a == Hash("") {
		t.Error("Hash of content collided with Hash of empty")
	}
}

func TestHashWidth(t *testing.T) {
	if got := len(Hash("anything")); got != hashLen {
		t.Errorf("len(Hash()) = %d, want hashLen (%d)", got, hashLen)
	}
}
