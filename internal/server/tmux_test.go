package server

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type tmuxRecordingTerminal struct {
	mu     sync.Mutex
	writes []string
}

func (terminal *tmuxRecordingTerminal) Read([]byte) (int, error) { return 0, io.EOF }
func (terminal *tmuxRecordingTerminal) Write(data []byte) (int, error) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.writes = append(terminal.writes, string(data))
	return len(data), nil
}
func (terminal *tmuxRecordingTerminal) Resize(_, _ uint16) error { return nil }
func (terminal *tmuxRecordingTerminal) Close() error             { return nil }
func (terminal *tmuxRecordingTerminal) recordedWrites() []string {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return append([]string(nil), terminal.writes...)
}

type tmuxCommandCall struct {
	name string
	args []string
}

func TestValidTmuxSessionName(t *testing.T) {
	valid := []string{
		"a",
		"Session-01",
		"work.main_2",
		strings.Repeat("a", 64),
	}
	for _, name := range valid {
		if !validTmuxSessionName(name) {
			t.Errorf("validTmuxSessionName(%q) = false, want true", name)
		}
	}

	invalid := []string{
		"",
		"-starts-with-dash",
		"_starts_with_underscore",
		"contains space",
		"contains/slash",
		"contains:colon",
		strings.Repeat("a", 65),
	}
	for _, name := range invalid {
		if validTmuxSessionName(name) {
			t.Errorf("validTmuxSessionName(%q) = true, want false", name)
		}
	}
}

func TestParseTmuxSessions(t *testing.T) {
	createdAlpha := int64(1_700_000_000)
	activityAlpha := int64(1_700_000_100)
	createdZeta := int64(1_600_000_000)
	activityZeta := int64(1_600_000_200)
	output := fmt.Sprintf(
		"zeta\t2\t0\t%d\t%d\r\nalpha\t3\t1\t%d\t%d\n",
		createdZeta,
		activityZeta,
		createdAlpha,
		activityAlpha,
	)

	sessions, err := parseTmuxSessions([]byte(output))
	if err != nil {
		t.Fatalf("parseTmuxSessions returned error: %v", err)
	}
	want := []tmuxSession{
		{
			Name:         "alpha",
			Windows:      3,
			Attached:     true,
			CreatedAt:    time.Unix(createdAlpha, 0).Format(time.RFC3339),
			LastActivity: time.Unix(activityAlpha, 0).Format(time.RFC3339),
		},
		{
			Name:         "zeta",
			Windows:      2,
			Attached:     false,
			CreatedAt:    time.Unix(createdZeta, 0).Format(time.RFC3339),
			LastActivity: time.Unix(activityZeta, 0).Format(time.RFC3339),
		},
	}
	if !reflect.DeepEqual(sessions, want) {
		t.Fatalf("parseTmuxSessions() = %#v, want %#v", sessions, want)
	}

	empty, err := parseTmuxSessions(nil)
	if err != nil {
		t.Fatalf("parseTmuxSessions(nil) returned error: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("parseTmuxSessions(nil) = %#v, want non-nil empty slice", empty)
	}

	tests := []struct {
		name   string
		output string
		match  string
	}{
		{name: "field count", output: "main\t1\t0\t1700000000", match: "unexpected tmux output"},
		{name: "window count", output: "main\tnot-a-number\t0\t1700000000\t1700000001", match: "invalid tmux window count"},
		{name: "creation time", output: "main\t1\t0\tnot-a-number\t1700000001", match: "invalid tmux creation time"},
		{name: "activity time", output: "main\t1\t0\t1700000000\tnot-a-number", match: "invalid tmux activity time"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseTmuxSessions([]byte(test.output))
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("parseTmuxSessions(%q) error = %v, want containing %q", test.output, err, test.match)
			}
		})
	}
}

func TestParseTmuxWindows(t *testing.T) {
	windows, err := parseTmuxWindows([]byte("0\teditor\t1\t2\r\n3\tlogs\t0\t1\n"))
	if err != nil {
		t.Fatalf("parseTmuxWindows returned error: %v", err)
	}
	want := []tmuxWindow{
		{Index: 0, Name: "editor", Active: true, Panes: 2},
		{Index: 3, Name: "logs", Active: false, Panes: 1},
	}
	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("parseTmuxWindows() = %#v, want %#v", windows, want)
	}

	empty, err := parseTmuxWindows(nil)
	if err != nil {
		t.Fatalf("parseTmuxWindows(nil) returned error: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("parseTmuxWindows(nil) = %#v, want non-nil empty slice", empty)
	}

	tests := []struct {
		name   string
		output string
		match  string
	}{
		{name: "field count", output: "0\teditor\t1", match: "unexpected tmux window output"},
		{name: "window index", output: "bad\teditor\t1\t2", match: "invalid tmux window index"},
		{name: "pane count", output: "0\teditor\t1\tbad", match: "invalid tmux pane count"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseTmuxWindows([]byte(test.output))
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("parseTmuxWindows(%q) error = %v, want containing %q", test.output, err, test.match)
			}
		})
	}
}

func TestPersistentSessionTmuxTargetLifecycle(t *testing.T) {
	session := &persistentSession{}
	if target := session.currentTmuxTarget(); target != nil {
		t.Fatalf("initial target = %#v, want nil", target)
	}

	session.setTmuxTarget(tmuxTarget{Session: "main", Window: 2, Name: "editor"})
	target := session.currentTmuxTarget()
	want := &tmuxTarget{Session: "main", Window: 2, Name: "editor"}
	if !reflect.DeepEqual(target, want) {
		t.Fatalf("current target = %#v, want %#v", target, want)
	}

	target.Session = "mutated-copy"
	if got := session.currentTmuxTarget(); got.Session != "main" {
		t.Fatalf("mutating returned target changed stored target to %#v", got)
	}

	session.renameTmuxTarget("other", "ignored")
	if got := session.currentTmuxTarget(); got.Session != "main" {
		t.Fatalf("unrelated rename changed target to %#v", got)
	}
	session.renameTmuxTarget("main", "renamed")
	if got := session.currentTmuxTarget(); got.Session != "renamed" || got.Window != 2 {
		t.Fatalf("renamed target = %#v, want session renamed and window 2", got)
	}

	session.clearTmuxTarget("other")
	if got := session.currentTmuxTarget(); got == nil {
		t.Fatal("unrelated clear removed target")
	}
	session.clearTmuxTarget("renamed")
	if got := session.currentTmuxTarget(); got != nil {
		t.Fatalf("cleared target = %#v, want nil", got)
	}
}

func newTmuxSwitchTestServer(t *testing.T) (*Server, *tmuxRecordingTerminal, *[]tmuxCommandCall) {
	t.Helper()
	server, _ := newTestServer(t)
	terminal := &tmuxRecordingTerminal{}
	server.session = &persistentSession{terminal: terminal, closed: make(chan struct{})}
	calls := &[]tmuxCommandCall{}
	server.runCommand = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, tmuxCommandCall{name: name, args: append([]string(nil), args...)})
		return nil, nil
	}
	return server, terminal, calls
}

func TestSwitchTmuxTargetInitialAttachOnlyOnce(t *testing.T) {
	server, terminal, calls := newTmuxSwitchTestServer(t)

	if err := server.switchTmuxTarget("main", nil); err != nil {
		t.Fatalf("first switchTmuxTarget returned error: %v", err)
	}
	if err := server.switchTmuxTarget("main", nil); err != nil {
		t.Fatalf("second switchTmuxTarget returned error: %v", err)
	}

	if got, want := terminal.recordedWrites(), []string{"tmux attach-session -t main\n"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("terminal writes = %#v, want %#v", got, want)
	}
	for _, call := range *calls {
		if len(call.args) > 0 && call.args[0] == "switch-client" {
			t.Fatalf("same-session second switch unexpectedly called switch-client: %#v", call)
		}
	}
	if got := server.currentTmuxTarget(); got == nil || got.Session != "main" || got.Window != 0 {
		t.Fatalf("current target = %#v, want main window 0", got)
	}
}

func TestSwitchTmuxTargetUsesSwitchClient(t *testing.T) {
	server, terminal, calls := newTmuxSwitchTestServer(t)
	server.session.setTmuxTarget(tmuxTarget{Session: "old", Window: 4})

	if err := server.switchTmuxTarget("new", nil); err != nil {
		t.Fatalf("switchTmuxTarget returned error: %v", err)
	}
	if got := terminal.recordedWrites(); len(got) != 0 {
		t.Fatalf("terminal writes = %#v, want none after initial attachment", got)
	}

	wantCall := tmuxCommandCall{name: "tmux", args: []string{"switch-client", "-t", "new"}}
	found := false
	for _, call := range *calls {
		if reflect.DeepEqual(call, wantCall) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("commands = %#v, want %#v", *calls, wantCall)
	}
	if got := server.currentTmuxTarget(); got == nil || got.Session != "new" || got.Window != 0 {
		t.Fatalf("current target = %#v, want new window 0", got)
	}
}

func TestSwitchTmuxTargetSelectsWindow(t *testing.T) {
	server, terminal, calls := newTmuxSwitchTestServer(t)
	server.session.setTmuxTarget(tmuxTarget{Session: "main", Window: 1})
	window := 7

	if err := server.switchTmuxTarget("main", &window); err != nil {
		t.Fatalf("switchTmuxTarget returned error: %v", err)
	}
	if got := terminal.recordedWrites(); len(got) != 0 {
		t.Fatalf("terminal writes = %#v, want none after initial attachment", got)
	}

	wantCall := tmuxCommandCall{name: "tmux", args: []string{"select-window", "-t", "main:7"}}
	found := false
	for _, call := range *calls {
		if reflect.DeepEqual(call, wantCall) {
			found = true
		}
		if len(call.args) > 0 && call.args[0] == "switch-client" {
			t.Fatalf("same-session window selection unexpectedly called switch-client: %#v", call)
		}
	}
	if !found {
		t.Fatalf("commands = %#v, want %#v", *calls, wantCall)
	}
	if got := server.currentTmuxTarget(); got == nil || got.Session != "main" || got.Window != 7 {
		t.Fatalf("current target = %#v, want main window 7", got)
	}
}
