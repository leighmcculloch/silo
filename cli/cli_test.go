package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogTo(t *testing.T) {
	var buf bytes.Buffer
	LogTo(&buf, "test message")

	output := buf.String()
	if !strings.Contains(output, "test message") {
		t.Errorf("expected output to contain 'test message', got: %s", output)
	}
	if !strings.Contains(output, "==>") {
		t.Errorf("expected output to contain '==>', got: %s", output)
	}
}

func TestLogSuccessTo(t *testing.T) {
	var buf bytes.Buffer
	LogSuccessTo(&buf, "success")

	output := buf.String()
	if !strings.Contains(output, "success") {
		t.Errorf("expected output to contain 'success', got: %s", output)
	}
	if !strings.Contains(output, "✓") {
		t.Errorf("expected output to contain checkmark, got: %s", output)
	}
}

func TestLogWarningTo(t *testing.T) {
	var buf bytes.Buffer
	LogWarningTo(&buf, "warning")

	output := buf.String()
	if !strings.Contains(output, "warning") {
		t.Errorf("expected output to contain 'warning', got: %s", output)
	}
	if !strings.Contains(output, "!") {
		t.Errorf("expected output to contain '!', got: %s", output)
	}
}

func TestLogErrorTo(t *testing.T) {
	var buf bytes.Buffer
	LogErrorTo(&buf, "error")

	output := buf.String()
	if !strings.Contains(output, "error") {
		t.Errorf("expected output to contain 'error', got: %s", output)
	}
	if !strings.Contains(output, "✗") {
		t.Errorf("expected output to contain '✗', got: %s", output)
	}
}

func TestLogBulletTo(t *testing.T) {
	var buf bytes.Buffer
	LogBulletTo(&buf, "item")

	output := buf.String()
	if !strings.Contains(output, "item") {
		t.Errorf("expected output to contain 'item', got: %s", output)
	}
}

func TestLogDimTo(t *testing.T) {
	var buf bytes.Buffer
	LogDimTo(&buf, "dimmed")

	output := buf.String()
	if !strings.Contains(output, "dimmed") {
		t.Errorf("expected output to contain 'dimmed', got: %s", output)
	}
}

func TestFormatting(t *testing.T) {
	var buf bytes.Buffer
	LogTo(&buf, "value: %d", 42)

	output := buf.String()
	if !strings.Contains(output, "value: 42") {
		t.Errorf("expected formatted output, got: %s", output)
	}
}

func TestTitle(t *testing.T) {
	title := Title("My Title")
	if title == "" {
		t.Error("expected non-empty title")
	}
	if !strings.Contains(title, "My Title") {
		t.Errorf("expected title to contain text, got: %s", title)
	}
}

func TestSubtitle(t *testing.T) {
	subtitle := Subtitle("My Subtitle")
	if subtitle == "" {
		t.Error("expected non-empty subtitle")
	}
	if !strings.Contains(subtitle, "My Subtitle") {
		t.Errorf("expected subtitle to contain text, got: %s", subtitle)
	}
}
