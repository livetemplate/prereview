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
}

// keyBindings is the keymap. Every entry with a non-empty Action becomes a
// global window keydown binding carrying lvt-mod:skip-when-typing, so a
// shortcut letter typed into the comment box (or any text field) types the
// letter instead of navigating. Esc and Enter have no Action — Esc has its own
// guard-free global binding in the template (so it cancels mid-typing) and
// Enter saves via the composer form — but both are listed so the help overlay
// is a complete reference.
var keyBindings = []KeyBinding{
	{Keys: []string{"j"}, Display: "j", Action: "nextFile", Label: "Next file"},
	{Keys: []string{"k"}, Display: "k", Action: "prevFile", Label: "Previous file"},
	{Keys: []string{"ArrowDown"}, Display: "↓", Action: "cursorDown", Label: "Move line cursor down"},
	{Keys: []string{"ArrowUp"}, Display: "↑", Action: "cursorUp", Label: "Move line cursor up"},
	{Keys: []string{"n"}, Display: "n", Action: "nextComment", Label: "Next comment"},
	{Keys: []string{"p"}, Display: "p", Action: "prevComment", Label: "Previous comment"},
	{Keys: []string{"c"}, Display: "c", Action: "openFileComment", Label: "Comment on this file"},
	{Keys: []string{"f"}, Display: "f", Action: "toggleFiles", Label: "Toggle file tree"},
	{Keys: []string{"a"}, Display: "a", Action: "toggleCommentList", Label: "All comments"},
	{Keys: []string{"r"}, Display: "r", Action: "toggleShowResolved", Label: "Show / hide resolved comments"},
	{Keys: []string{"."}, Display: ".", Action: "toggleFocusMode", Label: "Focus mode (hide side columns)"},
	{Keys: []string{"?"}, Display: "?", Action: "toggleKeyboardHelp", Label: "Keyboard shortcuts"},
	{Keys: []string{"Escape"}, Display: "Esc", Action: "", Label: "Cancel / close"},
	{Keys: []string{"Enter"}, Display: "Enter", Action: "", Label: "Comment on the line cursor (or save the draft)"},
}

// KeyBindings exposes the keymap to the template. Zero-arg so livetemplate's
// evaluator auto-invokes it; the template ranges over it for both the hidden
// window bindings and the help overlay.
func (s PrereviewState) KeyBindings() []KeyBinding { return keyBindings }
