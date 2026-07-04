package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

func TestNativeToolsBoundFileReadsAndBinary(t *testing.T) {
	workspace := t.TempDir()
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i+1))
	}
	if err := os.WriteFile(filepath.Join(workspace, "big.txt"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "bin.dat"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "invalid.txt"), []byte{0xff, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_NATIVE_TOOL_MAX_LINES", "5")
	t.Setenv("TELOS_NATIVE_TOOL_MAX_BYTES", "64")
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	read := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "read_file",
		Arguments: `{"path":"big.txt","start_line":3,"limit_lines":50}`,
	})
	if read.IsError {
		t.Fatalf("read failed: %+v", read)
	}
	if !strings.Contains(read.Output, "lines_returned: 3-7") || !strings.Contains(read.Output, "truncated: true") {
		t.Fatalf("bounded read output missing metadata:\n%s", read.Output)
	}

	binary := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_2",
		Name:      "read_file",
		Arguments: `{"path":"bin.dat"}`,
	})
	if binary.IsError || !strings.Contains(binary.Output, "binary: true") {
		t.Fatalf("binary output: %+v", binary)
	}

	invalid := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_3",
		Name:      "read_file",
		Arguments: `{"path":"invalid.txt"}`,
	})
	if invalid.IsError || !strings.Contains(invalid.Output, "binary: true") {
		t.Fatalf("invalid UTF-8 output: %+v", invalid)
	}

	truncatedText, ok := truncateText("aaébb", 3)
	if !ok || !utf8.ValidString(truncatedText) || strings.ContainsRune(truncatedText, utf8.RuneError) {
		t.Fatalf("truncateText should preserve valid UTF-8, got %q truncated=%t", truncatedText, ok)
	}
}

func TestReadTextFileRangeAcceptsUTF8SplitAcrossBuffer(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "long.txt")
	line := strings.Repeat("a", 64<<10-1) + "é\nsecond\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	content, totalLines, endLine, truncated, binary, err := readTextFileRange(path, 1, 1, 80<<10)
	if err != nil {
		t.Fatal(err)
	}
	if binary {
		t.Fatal("valid UTF-8 split across reader buffer was classified as binary")
	}
	if truncated {
		t.Fatalf("unexpected truncation")
	}
	if totalLines != 2 || endLine != 1 {
		t.Fatalf("line metadata: total=%d end=%d", totalLines, endLine)
	}
	if !utf8.ValidString(content) || !strings.Contains(content, "é") {
		t.Fatalf("content should remain valid and include split rune: valid=%t contains=%t", utf8.ValidString(content), strings.Contains(content, "é"))
	}
}

func TestNativeToolIntegerArgumentsRejectFractions(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "big.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_fraction",
		Name:      "read_file",
		Arguments: `{"path":"big.txt","limit_lines":0.5}`,
	})
	if !result.IsError {
		t.Fatalf("expected fractional integer argument to fail:\n%s", result.Output)
	}
	if result.ErrorCode != errAgentProtocol || !strings.Contains(result.Output, `argument "limit_lines" must be of type integer`) {
		t.Fatalf("unexpected error: code=%q output=%s", result.ErrorCode, result.Output)
	}
}

func TestNativeEditingToolsPreserveExistingFileMode(t *testing.T) {
	workspace := t.TempDir()
	scriptPath := filepath.Join(workspace, "script.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	written := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_write",
		Name:      "write_file",
		Arguments: `{"path":"script.sh","content":"#!/bin/sh\necho new\n"}`,
	})
	if written.IsError {
		t.Fatalf("write_file failed:\n%s", written.Output)
	}
	if !strings.Contains(written.Output, "created: false") || !strings.Contains(written.Output, "mode: -rwxr-xr-x") {
		t.Fatalf("write_file output missing mode metadata:\n%s", written.Output)
	}
	if info, err := os.Stat(scriptPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o755 {
		t.Fatalf("write_file should preserve mode, got %o", info.Mode().Perm())
	}

	replaced := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_replace",
		Name:      "replace_text",
		Arguments: `{"path":"script.sh","old_string":"echo new","new_string":"echo newer","expected_count":1}`,
	})
	if replaced.IsError {
		t.Fatalf("replace_text failed:\n%s", replaced.Output)
	}
	if !strings.Contains(replaced.Output, "replacement_count: 1") || !strings.Contains(replaced.Output, "mode: -rwxr-xr-x") {
		t.Fatalf("replace_text output missing mode metadata:\n%s", replaced.Output)
	}
	if info, err := os.Stat(scriptPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o755 {
		t.Fatalf("replace_text should preserve mode, got %o", info.Mode().Perm())
	}
}

func TestNativeToolsReplaceTextRejectsBinaryAndInvalidUTF8(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "nul.bin"), []byte{'o', 'l', 'd', 0, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "invalid.txt"), []byte{0xff, 'o', 'l', 'd'}, 0o644); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	for _, path := range []string{"nul.bin", "invalid.txt"} {
		result := tools.execute(context.Background(), nativeToolCall{
			ID:        "call_" + path,
			Name:      "replace_text",
			Arguments: `{"path":"` + path + `","old_string":"old","new_string":"new"}`,
		})
		if !result.IsError {
			t.Fatalf("expected replace_text to reject %s:\n%s", path, result.Output)
		}
		if !strings.Contains(result.Output, "not a UTF-8 text file") {
			t.Fatalf("unexpected replace_text output for %s:\n%s", path, result.Output)
		}
	}
}

func TestNativeEditingToolsRejectNULTextInputs(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "plain.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	written := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_write",
		Name:      "write_file",
		Arguments: "{\"path\":\"nul.txt\",\"content\":\"a\\u0000b\"}",
	})
	if !written.IsError || !strings.Contains(written.Output, "contains NUL byte") {
		t.Fatalf("expected write_file to reject NUL content:\n%s", written.Output)
	}

	replaced := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_replace",
		Name:      "replace_text",
		Arguments: "{\"path\":\"plain.txt\",\"old_string\":\"old\",\"new_string\":\"new\\u0000\"}",
	})
	if !replaced.IsError || !strings.Contains(replaced.Output, "must not contain NUL bytes") {
		t.Fatalf("expected replace_text to reject NUL content:\n%s", replaced.Output)
	}
}

func TestNativeToolsBashReportsOriginalOutputCounts(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "bash",
		Arguments: `{"command":"python3 - <<'PY'\nimport sys\nprint('x' * (300 * 1024))\nprint('y' * (300 * 1024), file=sys.stderr)\nPY"}`,
	})
	if result.IsError {
		t.Fatalf("bash failed:\n%s", result.Output)
	}
	if !result.Truncated {
		t.Fatalf("expected bash result metadata to mark truncation: %+v", result)
	}
	if !result.HasExitCode || result.ExitCode != 0 {
		t.Fatalf("expected bash exit-code metadata, got %+v", result)
	}
	if result.Metadata["stdout_truncated"] != true || result.Metadata["stderr_truncated"] != true {
		t.Fatalf("expected structured truncation metadata: %#v", result.Metadata)
	}
	if _, ok := result.Metadata["stdout_original_bytes"].(int); !ok {
		t.Fatalf("expected stdout_original_bytes metadata: %#v", result.Metadata)
	}
	for _, want := range []string{
		"stdout_original_bytes:",
		"stdout_original_lines: 1",
		"stdout_truncated: true",
		"stderr_original_bytes:",
		"stderr_original_lines: 1",
		"stderr_truncated: true",
		"signal: none",
		"started_at:",
		"ended_at:",
		"timed_out: false",
		"interrupted: false",
	} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("bash output missing %q:\n%s", want, result.Output)
		}
	}
}

func TestNativeToolsSearchTextStopsAtMatchLimit(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a-first.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "z-later.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "search_text",
		Arguments: `{"pattern":"needle","max_matches":1}`,
	})

	if result.IsError {
		t.Fatalf("search_text failed:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "match_count: 1") || !strings.Contains(result.Output, "truncated: true") {
		t.Fatalf("search output missing cap metadata:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "a-first.txt") {
		t.Fatalf("expected first file match:\n%s", result.Output)
	}
	if strings.Contains(result.Output, "z-later.txt") {
		t.Fatalf("search should stop after max_matches:\n%s", result.Output)
	}
}

func TestNativeToolsListDirReturnsBoundedEntriesWithTotalCount(t *testing.T) {
	workspace := t.TempDir()
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("file-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TELOS_NATIVE_TOOL_MAX_LINES", "5")
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "list_dir",
		Arguments: `{}`,
	})

	if result.IsError {
		t.Fatalf("list_dir failed:\n%s", result.Output)
	}
	for _, want := range []string{
		"entry_count: 12",
		"entries_returned: 5",
		"truncated: true",
	} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("list_dir output missing %q:\n%s", want, result.Output)
		}
	}
	if result.Metadata["entries_returned"] != 5 || result.Metadata["entry_count"] != 12 || result.Metadata["truncated"] != true {
		t.Fatalf("list_dir metadata: %#v", result.Metadata)
	}
}

func TestNativeToolsListDirAndFindFilesApplyByteCaps(t *testing.T) {
	workspace := t.TempDir()
	longNames := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("long-file-%02d-%s.txt", i, strings.Repeat("x", 60))
		longNames = append(longNames, name)
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TELOS_NATIVE_TOOL_MAX_BYTES", "128")
	t.Setenv("TELOS_NATIVE_TOOL_MAX_LINES", "20")
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	listed := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_list",
		Name:      "list_dir",
		Arguments: `{}`,
	})
	if listed.IsError {
		t.Fatalf("list_dir failed:\n%s", listed.Output)
	}
	if !strings.Contains(listed.Output, "entries_returned: 6") || !strings.Contains(listed.Output, "truncated: true") {
		t.Fatalf("list_dir should report byte truncation without entry truncation:\n%s", listed.Output)
	}
	if strings.Contains(listed.Output, longNames[len(longNames)-1]) {
		t.Fatalf("list_dir output should be byte capped:\n%s", listed.Output)
	}

	found := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_find",
		Name:      "find_files",
		Arguments: `{"pattern":"*.txt","max_matches":20}`,
	})
	if found.IsError {
		t.Fatalf("find_files failed:\n%s", found.Output)
	}
	if !strings.Contains(found.Output, "match_count: 6") || !strings.Contains(found.Output, "truncated: true") {
		t.Fatalf("find_files should report byte truncation without match truncation:\n%s", found.Output)
	}
	if strings.Contains(found.Output, longNames[len(longNames)-1]) {
		t.Fatalf("find_files output should be byte capped:\n%s", found.Output)
	}
}

func TestNativeToolsFindFilesSupportsRecursiveGlobstar(t *testing.T) {
	workspace := t.TempDir()
	// Build a nested tree so we can exercise `**` across directory boundaries.
	files := map[string]string{
		"a.go":                        "x",
		"pkg/b.go":                    "x",
		"pkg/sub/c.go":                "x",
		"pkg/sub/deep/d.go":           "x",
		"pkg/sub/deep/e.txt":          "x",
		"other/also.go":               "x",
		"node_modules/dep/ignored.go": "x", // shouldSkipDir drops node_modules
	}
	for rel, content := range files {
		full := filepath.Join(workspace, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	// `**/*.go` must match .go files at every depth, including the root.
	r1 := tools.execute(context.Background(), nativeToolCall{
		ID: "call_1", Name: "find_files",
		Arguments: `{"pattern":"**/*.go","max_matches":50}`,
	})
	if r1.IsError {
		t.Fatalf("find_files **/*.go failed:\n%s", r1.Output)
	}
	want := []string{"a.go", "pkg/b.go", "pkg/sub/c.go", "pkg/sub/deep/d.go", "other/also.go"}
	if !strings.Contains(r1.Output, "match_count: 5") {
		t.Fatalf("expected 5 .go matches (node_modules skipped), got:\n%s", r1.Output)
	}
	for _, w := range want {
		if !strings.Contains(r1.Output, w) {
			t.Fatalf("expected %q in **/*.go results:\n%s", w, r1.Output)
		}
	}
	if strings.Contains(r1.Output, "ignored.go") {
		t.Fatalf("node_modules should be skipped:\n%s", r1.Output)
	}

	// A scoped recursive pattern under a specific directory.
	r2 := tools.execute(context.Background(), nativeToolCall{
		ID: "call_2", Name: "find_files",
		Arguments: `{"pattern":"pkg/**/*.go","max_matches":50}`,
	})
	if r2.IsError {
		t.Fatalf("find_files pkg/**/*.go failed:\n%s", r2.Output)
	}
	if !strings.Contains(r2.Output, "match_count: 3") {
		t.Fatalf("expected 3 matches under pkg/, got:\n%s", r2.Output)
	}
	for _, w := range []string{"pkg/b.go", "pkg/sub/c.go", "pkg/sub/deep/d.go"} {
		if !strings.Contains(r2.Output, w) {
			t.Fatalf("expected %q in pkg/**/*.go results:\n%s", w, r2.Output)
		}
	}

	// A bare `*.go` still matches at any depth (basename fallback).
	r3 := tools.execute(context.Background(), nativeToolCall{
		ID: "call_3", Name: "find_files",
		Arguments: `{"pattern":"*.go","max_matches":50}`,
	})
	if r3.IsError {
		t.Fatalf("find_files *.go failed:\n%s", r3.Output)
	}
	if !strings.Contains(r3.Output, "match_count: 5") {
		t.Fatalf("bare *.go should match at any depth, got:\n%s", r3.Output)
	}

	// An invalid pattern is rejected up front.
	r4 := tools.execute(context.Background(), nativeToolCall{
		ID: "call_4", Name: "find_files",
		Arguments: `{"pattern":"[unclosed","max_matches":50}`,
	})
	if !r4.IsError {
		t.Fatalf("invalid glob pattern should be rejected, got:\n%s", r4.Output)
	}
}

func TestNativeToolsBashAcceptsBoundedEnv(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "bash",
		Arguments: `{"command":"printf '%s' \"$TELOS_TEST_VALUE\"","env":{"TELOS_TEST_VALUE":"from-tool"}}`,
	})
	if result.IsError {
		t.Fatalf("bash failed:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "from-tool") {
		t.Fatalf("bash output missing env value:\n%s", result.Output)
	}
}

func TestNativeToolsBashRejectsInvalidEnvName(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "bash",
		Arguments: `{"command":"true","env":{"BAD-NAME":"value"}}`,
	})
	if !result.IsError {
		t.Fatalf("expected invalid env error, got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "invalid environment variable name") {
		t.Fatalf("unexpected output:\n%s", result.Output)
	}
}

func TestNativeToolsClassifiesMalformedToolCallAsAgentProtocol(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	invalidJSON := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_invalid",
		Name:      "bash",
		Arguments: `{"command":`,
	})
	if !invalidJSON.IsError || invalidJSON.ErrorCode != errAgentProtocol {
		t.Fatalf("invalid JSON classification: %+v\n%s", invalidJSON, invalidJSON.Output)
	}

	unknown := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_unknown",
		Name:      "does_not_exist",
		Arguments: `{}`,
	})
	if !unknown.IsError || unknown.ErrorCode != errAgentProtocol {
		t.Fatalf("unknown tool classification: %+v\n%s", unknown, unknown.Output)
	}
}

func TestNativeToolsBashMarksNonzeroExitAsError(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "bash",
		Arguments: `{"command":"printf out; printf err >&2; exit 7"}`,
	})

	if !result.IsError {
		t.Fatalf("expected nonzero bash exit to be an error:\n%s", result.Output)
	}
	if !result.HasExitCode || result.ExitCode != 7 {
		t.Fatalf("exit-code metadata: %+v\n%s", result, result.Output)
	}
	if result.ErrorCode != "" {
		t.Fatalf("nonzero command exit should not be classified as tool infra: %+v", result)
	}
	for _, want := range []string{"ok: false", "exit_code: 7", "stdout:\nout", "stderr:\nerr"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("bash output missing %q:\n%s", want, result.Output)
		}
	}
}

func TestNativeToolsBashAppliesLineCaps(t *testing.T) {
	t.Setenv("TELOS_NATIVE_TOOL_MAX_LINES", "3")
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_lines",
		Name:      "bash",
		Arguments: `{"command":"for i in 1 2 3 4 5; do echo out-$i; echo err-$i >&2; done"}`,
	})

	if result.IsError {
		t.Fatalf("bash failed:\n%s", result.Output)
	}
	for _, want := range []string{
		"stdout_original_lines: 5",
		"stderr_original_lines: 5",
		"stdout_truncated: true",
		"stderr_truncated: true",
		"... stdout truncated at 3 lines of 5 ...",
		"... stderr truncated at 3 lines of 5 ...",
	} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("bash line-cap output missing %q:\n%s", want, result.Output)
		}
	}
	if strings.Contains(result.Output, "out-4") || strings.Contains(result.Output, "err-4") {
		t.Fatalf("line-capped output leaked lines beyond cap:\n%s", result.Output)
	}
	if result.Metadata["stdout_truncated"] != true || result.Metadata["stderr_truncated"] != true {
		t.Fatalf("truncation metadata: %#v", result.Metadata)
	}
}

func TestNativeToolsBashClassifiesTimeoutAsToolTimeout(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_timeout",
		Name:      "bash",
		Arguments: `{"command":"sleep 60","timeout_seconds":1}`,
	})

	if !result.IsError {
		t.Fatalf("expected timeout to be an error:\n%s", result.Output)
	}
	if result.ErrorCode != errToolTimeout {
		t.Fatalf("timeout error code: got %q\n%s", result.ErrorCode, result.Output)
	}
	for _, want := range []string{"timed_out: true", "error:\nlocal_timeout:1"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("timeout output missing %q:\n%s", want, result.Output)
		}
	}
}

func TestNativeToolsBashTimeoutCapUsesTurnBudget(t *testing.T) {
	workspace := t.TempDir()
	tests := []struct {
		name   string
		budget game.TurnBudget
		want   int
	}{
		{name: "default", budget: game.TurnBudget{}, want: defaultToolTimeoutSec},
		{name: "agent timeout raises cap", budget: game.TurnBudget{AgentTimeoutSec: 600}, want: 600},
		{name: "remaining duration caps", budget: game.TurnBudget{AgentTimeoutSec: 600, RemainingDurationSec: 45}, want: 45},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), tt.budget)
			if got := tools.effectiveBashTimeoutCap(); got != tt.want {
				t.Fatalf("bash timeout cap: got %d want %d", got, tt.want)
			}
		})
	}
}

func TestNativeSessionLoggerIncludesToolErrorCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	logger := newNativeSessionLogger(path, dir)
	if err := logger.start(); err != nil {
		t.Fatal(err)
	}
	if err := logger.tool(nativeToolResult{CallID: "call_timeout", Name: "bash", Output: "timeout", IsError: true, ErrorCode: errToolTimeout}); err != nil {
		t.Fatal(err)
	}

	events := sessionLogEventsByType(t, path, "tool_result")
	if len(events) != 1 {
		t.Fatalf("tool_result events: %#v", events)
	}
	if events[0]["error_code"] != string(errToolTimeout) {
		t.Fatalf("tool_result error_code: %#v", events[0])
	}
}

func TestNativeSessionLoggerIncludesToolMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	logger := newNativeSessionLogger(path, dir)
	if err := logger.start(); err != nil {
		t.Fatal(err)
	}
	result := nativeToolResult{
		CallID:     "call_1",
		Name:       "bash",
		Output:     "tool: bash\nok: true\nduration_ms: 12\nstdout_original_bytes: 300000\nstdout_truncated: true\n",
		DurationMS: 12,
		Metadata: map[string]any{
			"stdout_original_bytes": 300000,
			"stdout_truncated":      true,
		},
	}
	if err := logger.tool(result); err != nil {
		t.Fatal(err)
	}

	events := sessionLogEventsByType(t, path, "tool_result")
	if len(events) != 1 {
		t.Fatalf("tool_result events: %#v", events)
	}
	metadata, ok := events[0]["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata missing: %#v", events[0])
	}
	if metadata["stdout_truncated"] != true || metadata["stdout_original_bytes"] != float64(300000) {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestNativeToolsApplyPatchReportsStructuredMetadata(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "old.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})
	patch := strings.Join([]string{
		"diff --git a/old.txt b/old.txt",
		"--- a/old.txt",
		"+++ b/old.txt",
		"@@ -1 +1 @@",
		"-before",
		"+after",
		"diff --git a/new.txt b/new.txt",
		"new file mode 100644",
		"index 0000000..8d1c8b2",
		"--- /dev/null",
		"+++ b/new.txt",
		"@@ -0,0 +1 @@",
		"+created",
		"",
	}, "\n")

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "apply_patch",
		Arguments: `{"patch":` + mustJSON(patch) + `}`,
	})

	if result.IsError {
		t.Fatalf("apply_patch failed:\n%s", result.Output)
	}
	for _, want := range []string{
		"patch_bytes:",
		"changed_path_count: 2",
		"changed_paths: new.txt, old.txt",
		"created_paths: new.txt",
		"hunk_count: 2",
		"files:",
		"- path: new.txt",
		"created: true",
		"bytes_written: 8",
		"- path: old.txt",
		"created: false",
		"bytes_written: 6",
		"mode: -rw-r--r--",
	} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("apply_patch output missing %q:\n%s", want, result.Output)
		}
	}
	updated, err := os.ReadFile(filepath.Join(workspace, "old.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "after\n" {
		t.Fatalf("old.txt: got %q", updated)
	}
	created, err := os.ReadFile(filepath.Join(workspace, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(created) != "created\n" {
		t.Fatalf("new.txt: got %q", created)
	}
}

func TestNativeToolsApplyPatchRejectsUnsafePaths(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs(), game.TurnBudget{})
	cases := []struct {
		name  string
		patch string
	}{
		{
			name: "hunk path",
			patch: strings.Join([]string{
				"diff --git a/../escape.txt b/../escape.txt",
				"new file mode 100644",
				"index 0000000..8d1c8b2",
				"--- /dev/null",
				"+++ b/../escape.txt",
				"@@ -0,0 +1 @@",
				"+created",
				"",
			}, "\n"),
		},
		{
			name: "diff header",
			patch: strings.Join([]string{
				"diff --git a/safe.txt b/../escape.txt",
				"--- a/safe.txt",
				"+++ b/safe.txt",
				"@@ -1 +1 @@",
				"-before",
				"+after",
				"",
			}, "\n"),
		},
		{
			name: "rename header",
			patch: strings.Join([]string{
				"diff --git a/safe.txt b/safe2.txt",
				"similarity index 100%",
				"rename from safe.txt",
				"rename to ../escape.txt",
				"",
			}, "\n"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := tools.execute(context.Background(), nativeToolCall{
				ID:        "call_1",
				Name:      "apply_patch",
				Arguments: `{"patch":` + mustJSON(tc.patch) + `}`,
			})

			if !result.IsError {
				t.Fatalf("expected unsafe patch path to fail:\n%s", result.Output)
			}
			if !strings.Contains(result.Output, "outside workspace") {
				t.Fatalf("unexpected output:\n%s", result.Output)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(workspace), "escape.txt")); !os.IsNotExist(err) {
				t.Fatalf("unsafe path was created: %v", err)
			}
		})
	}
}

func TestNativeSkillToolListsAndReadsSkillBodies(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(t.TempDir(), "review-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	refDir := filepath.Join(skillDir, "references")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	body := "---\nname: review-skill\ndescription: Review rubric\n---\n# Rubric\n\nCheck evidence.\nSee [details](references/details.md).\n"
	if err := os.WriteFile(skillPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "details.md"), []byte("# Details\n\nInspect logs.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := []game.TurnSkill{{Name: "review-skill", Description: "Review rubric", SkillPath: filepath.ToSlash(skillPath), Required: true}}
	logPath := filepath.Join(workspace, ".turn", "session.jsonl")
	logger := newNativeSessionLogger(logPath, workspace)
	if err := logger.start(); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, skills, logger, resolveEnvKnobs(), game.TurnBudget{})

	list := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "skill",
		Arguments: `{"action":"list"}`,
	})
	if list.IsError || !strings.Contains(list.Output, "name: review-skill") || !strings.Contains(list.Output, "required: true") {
		t.Fatalf("skill list output: %+v", list)
	}

	read := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_2",
		Name:      "skill",
		Arguments: `{"action":"read","name":"review-skill"}`,
	})
	if read.IsError || !strings.Contains(read.Output, "# Rubric") || !strings.Contains(read.Output, "Check evidence.") {
		t.Fatalf("skill read output: %+v", read)
	}
	if missing := tools.missingRequiredSkills(); len(missing) != 0 {
		t.Fatalf("required skill should be satisfied after complete read: %v", missing)
	}
	if events := sessionLogEventsByType(t, logPath, "skill_opened"); len(events) != 1 || events[0]["name"] != "review-skill" || events[0]["truncated"] != false {
		t.Fatalf("skill_opened events: %#v", events)
	}
	if events := sessionLogEventsByType(t, logPath, "skill_applied"); len(events) != 1 || events[0]["name"] != "review-skill" {
		t.Fatalf("skill_applied events: %#v", events)
	}

	refRead := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_3",
		Name:      "skill",
		Arguments: `{"action":"read_ref","name":"review-skill","path":"references/details.md"}`,
	})
	if refRead.IsError || !strings.Contains(refRead.Output, "# Details") || !strings.Contains(refRead.Output, "Inspect logs.") {
		t.Fatalf("skill reference read output: %+v", refRead)
	}

	escape := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_4",
		Name:      "skill",
		Arguments: `{"action":"read_ref","name":"review-skill","path":"../outside.md"}`,
	})
	if !escape.IsError || !strings.Contains(escape.Output, "outside skill directory") {
		t.Fatalf("skill reference escape output: %+v", escape)
	}
}

func TestNativeSkillToolRequiresCompleteRequiredRubricRead(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(t.TempDir(), "review-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	body := "# Rubric\n\n" + strings.Repeat("criterion must be read\n", 20)
	if err := os.WriteFile(skillPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_NATIVE_TOOL_MAX_BYTES", "64")
	skills := []game.TurnSkill{{Name: "review-skill", Description: "Review rubric", SkillPath: filepath.ToSlash(skillPath), Required: true}}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, skills, nil, resolveEnvKnobs(), game.TurnBudget{})

	read := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "skill",
		Arguments: `{"action":"read","name":"review-skill"}`,
	})
	if read.IsError || !strings.Contains(read.Output, "truncated: true") {
		t.Fatalf("skill read output: %+v", read)
	}
	if missing := tools.missingRequiredSkills(); len(missing) != 1 || missing[0] != "review-skill" {
		t.Fatalf("required skill should remain missing after truncated read: %v", missing)
	}
}

func TestNativeSkillToolOversizedSingleLineRequiredRubricCanSatisfyGate(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(t.TempDir(), "review-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	body := strings.Repeat("oversized criterion ", 1000)
	if err := os.WriteFile(skillPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_NATIVE_TOOL_MAX_BYTES", "64")
	skills := []game.TurnSkill{{Name: "review-skill", Description: "Review rubric", SkillPath: filepath.ToSlash(skillPath), Required: true}}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, skills, nil, resolveEnvKnobs(), game.TurnBudget{})

	read := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "skill",
		Arguments: `{"action":"read","name":"review-skill"}`,
	})
	if read.IsError || !strings.Contains(read.Output, "truncated: true") {
		t.Fatalf("skill read output: %+v", read)
	}
	if missing := tools.missingRequiredSkills(); len(missing) != 0 {
		t.Fatalf("oversized single-line rubric should have a path through the read gate: %v", missing)
	}
}

func TestNativeSkillToolPaginatedRequiredRubricRead(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(t.TempDir(), "review-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	// A rubric larger than a single read window: it can only be read in pages.
	body := "# Rubric\n\n" + strings.Repeat("criterion must be read\n", 250)
	if err := os.WriteFile(skillPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_NATIVE_TOOL_MAX_LINES", "100")
	skills := []game.TurnSkill{{Name: "review-skill", Description: "Review rubric", SkillPath: filepath.ToSlash(skillPath), Required: true}}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, skills, nil, resolveEnvKnobs(), game.TurnBudget{})

	read := func(start int) nativeToolResult {
		return tools.execute(context.Background(), nativeToolCall{
			ID:        fmt.Sprintf("call_%d", start),
			Name:      "skill",
			Arguments: fmt.Sprintf(`{"action":"read","name":"review-skill","start_line":%d}`, start),
		})
	}

	// First page leaves the rubric still partially read.
	if r := read(1); r.IsError {
		t.Fatalf("page 1 read: %+v", r)
	}
	if missing := tools.missingRequiredSkills(); len(missing) != 1 {
		t.Fatalf("rubric should still be unread after first page: %v", missing)
	}
	// Walking start_line to EOF completes the read across pages.
	read(101)
	read(201)
	if missing := tools.missingRequiredSkills(); len(missing) != 0 {
		t.Fatalf("paginated read to EOF should satisfy the required rubric: %v", missing)
	}
}
