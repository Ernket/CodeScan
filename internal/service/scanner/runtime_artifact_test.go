package scanner

import (
	"strings"
	"testing"
)

func TestPrepareToolMessageForTranscriptStoresFullArtifact(t *testing.T) {
	useTestScannerConfig(t)

	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	content := strings.Repeat("x", toolTranscriptSafeBytes+512)
	transcriptContent, artifactID, err := prepareToolMessageForTranscript(session, "grep_files", content, "")
	if err != nil {
		t.Fatalf("prepare tool message for transcript: %v", err)
	}

	if artifactID == "" {
		t.Fatal("expected large tool output to create an artifact")
	}
	if !strings.Contains(transcriptContent, artifactID) {
		t.Fatalf("expected transcript content to reference artifact %s, got %q", artifactID, transcriptContent)
	}

	record, ok := session.loadArtifact(artifactID)
	if !ok {
		t.Fatalf("expected artifact %s to be retrievable", artifactID)
	}
	if len(record.Content) != len(content) {
		t.Fatalf("expected full artifact content to be preserved, got %d bytes want %d", len(record.Content), len(content))
	}
	if record.Truncated {
		t.Fatalf("expected full artifact preservation, got truncated artifact %+v", record)
	}
}
