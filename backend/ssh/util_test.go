package ssh

import "testing"

func TestShellQuoteEmpty(t *testing.T) {
	result := shellQuote("")
	if result != "''" {
		t.Errorf("expected '', got %q", result)
	}
}

func TestShellQuoteSafeString(t *testing.T) {
	result := shellQuote("/usr/local/bin")
	if result != "/usr/local/bin" {
		t.Errorf("expected unquoted path, got %q", result)
	}
}

func TestShellQuoteSafeChars(t *testing.T) {
	// All safe characters: letters, digits, / . - _ = :
	result := shellQuote("FOO=bar:1.0/path-name_x")
	if result != "FOO=bar:1.0/path-name_x" {
		t.Errorf("expected unquoted string, got %q", result)
	}
}

func TestShellQuoteWithSpaces(t *testing.T) {
	result := shellQuote("hello world")
	if result != "'hello world'" {
		t.Errorf("expected 'hello world', got %q", result)
	}
}

func TestShellQuoteWithSingleQuotes(t *testing.T) {
	result := shellQuote("it's")
	expected := "'it'\"'\"'s'"
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestShellQuoteWithSpecialChars(t *testing.T) {
	result := shellQuote("echo $HOME && rm -rf /")
	if result[0] != '\'' {
		t.Errorf("expected quoted string, got %q", result)
	}
	if result[len(result)-1] != '\'' {
		t.Errorf("expected quoted string to end with quote, got %q", result)
	}
}
