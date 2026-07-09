package review

// KeyBinding is one row of the keyboard map. It is the single source of truth
// for a shortcut: the template renders BOTH the hidden
// `lvt-on:window:keydown` elements (for bindings with a non-empty Action) and
// the rows of the keyboard-help overlay from the same slice, so the live
// bindings and their documentation can never drift apart.
type KeyBinding struct {
	// Keys are the raw KeyboardEvent.key values that trigger the action. Each
	// key emits its own hidden binding element (lvt-key matches one key), so a
	// shortcut with both a letter and an arrow (e.g. {"j", "ArrowDown"}) wires
	// two elements to the same Action.
	Keys []string

	// Display is how the keys read in the help overlay (e.g. "j  ↓"), kept
	// separate from Keys because the raw event.key value ("ArrowDown") isn't
	// what we want to show a human.
	Display string

	// Action is the controller method dispatched on keydown. Empty means the
	// shortcut is handled elsewhere and only documented here: Enter saves via
	// the composer form, and Esc has its own global window binding in the
	// template (a child of <body> so it works in both repo and external mode,
	// without the skip-when-typing guard so it cancels the composer
	// mid-typing). Help-only rows emit no hidden element.
	Action string

	// Label describes what the shortcut does, shown in the help overlay.
	Label string

	// Button, when set, is the form-button name (e.g. "addComment") that this
	// shortcut also triggers indirectly, so KeyHint can surface the key on that
	// button even though the shortcut isn't wired as a window Action. Used for
	// the composer's Save (Mod+Enter → the composer form) and Cancel (Esc → the
	// global clearSelection binding): both fire from their own template bindings,
	// not from the keyBindings loop, so their rows carry Button instead of Action.
	Button string

	// StreamOnly limits a binding to --stream (skill) mode. A stream-only
	// shortcut is filtered out of the live window bindings AND the help overlay
	// when the session isn't streaming, so a repo-only reviewer never sees a key
	// for a control (the agent-queue Pause/Resume) that doesn't exist for them.
	StreamOnly bool
}

// keyBindings is the keymap. Every entry with a non-empty Action becomes a
// global window keydown binding carrying lvt-mod:skip-when-typing, so a
// shortcut letter typed into the comment box (or any text field) types the
// letter instead of navigating. Esc, Enter, and Mod+Enter have no Action —
// each has its own binding in the template (Esc: guard-free cancel; Enter:
// comment on the cursor line; Mod+Enter: save from inside the composer, via
// lvt-key="Mod+Enter" on the form) — but all are listed so the help overlay is
// a complete reference.
var keyBindings = []KeyBinding{
	{Keys: []string{"j"}, Display: "j", Action: "nextFile", Label: "Next file"},
	{Keys: []string{"k"}, Display: "k", Action: "prevFile", Label: "Previous file"},
	{Keys: []string{"ArrowDown"}, Display: "↓", Action: "cursorDown", Label: "Move line cursor down"},
	{Keys: []string{"ArrowUp"}, Display: "↑", Action: "cursorUp", Label: "Move line cursor up"},
	{Keys: []string{"n"}, Display: "n", Action: "nextComment", Label: "Next comment"},
	{Keys: []string{"p"}, Display: "p", Action: "prevComment", Label: "Previous comment"},
	{Keys: []string{"c"}, Display: "c", Action: "openFileComment", Label: "Comment on this file"},
	{Keys: []string{"f"}, Display: "f", Action: "toggleFiles", Label: "Toggle file tree"},
	{Keys: []string{"Mod+k"}, Display: "⌘/Ctrl + K", Action: "openSearch", Label: "Search files"},
	{Keys: []string{"a"}, Display: "a", Action: "toggleCommentList", Label: "All comments"},
	{Keys: []string{"r"}, Display: "r", Action: "toggleShowResolved", Label: "Show / hide resolved comments"},
	{Keys: []string{"s"}, Display: "s", Action: "toggleSuggestions", Label: "Show / hide suggestions"},
	{Keys: []string{"q"}, Display: "q", Action: "toggleAgentPause", Label: "Pause / resume the agent queue", StreamOnly: true},
	{Keys: []string{"."}, Display: ".", Action: "toggleFocusMode", Label: "Focus mode (hide side columns)"},
	{Keys: []string{"?"}, Display: "?", Action: "toggleKeyboardHelp", Label: "Keyboard shortcuts"},
	{Keys: []string{"Escape"}, Display: "Esc", Action: "", Button: "clearSelection", Label: "Cancel / close"},
	{Keys: []string{"Enter"}, Display: "Enter", Action: "", Label: "Comment on the line cursor"},
	{Keys: []string{"Mod+Enter"}, Display: "⌘/Ctrl + Enter", Action: "", Button: "addComment", Label: "Save comment"},
}

// KeyBindings exposes the keymap to the template. Zero-arg so livetemplate's
// evaluator auto-invokes it; the template ranges over it for both the hidden
// window bindings and the help overlay. StreamOnly rows are dropped outside
// --stream mode so a repo-only reviewer gets neither a live binding nor a help
// row for the agent-queue Pause/Resume shortcut (there's no queue for them).
func (s PrereviewState) KeyBindings() []KeyBinding {
	if s.StreamMode {
		return keyBindings
	}
	out := make([]KeyBinding, 0, len(keyBindings))
	for _, b := range keyBindings {
		if b.StreamOnly {
			continue
		}
		out = append(out, b)
	}
	return out
}

// KeyHint maps a button name to its Display key, so a button that a shortcut
// fires can surface the same key in its label (issue #89 / #118) —
// {{with index $.KeyHint "toggleFocusMode"}}<kbd>{{.}}</kbd>{{end}}. Keyed by
// both Action (the window-bound rows: nextFile, toggleFocusMode, …) and Button
// (rows fired from their own template binding: Save via Mod+Enter → "addComment",
// Cancel via Esc → "clearSelection"). Plain Enter — documented in the help
// overlay but with no toolbar button — has neither, so it never leaks a chip.
// Single-sourced from the same keyBindings slice as the live bindings and the
// help overlay, so a chip can never show a stale key.
//
// Not filtered by StreamMode (unlike KeyBindings): a hint for a control that
// isn't rendered (the stream-only Pause button in repo mode) has no button to
// attach to, so it's harmless — and leaving it in keeps the lookup a pure map
// over the keymap. Zero-arg so livetemplate auto-invokes it (a method-with-arg
// would silently break rendering).
func (s PrereviewState) KeyHint() map[string]string {
	m := make(map[string]string, len(keyBindings))
	for _, b := range keyBindings {
		if b.Action != "" {
			m[b.Action] = b.Display
		}
		if b.Button != "" {
			m[b.Button] = b.Display
		}
	}
	return m
}
