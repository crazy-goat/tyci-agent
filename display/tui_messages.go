package display

type selectionFlashDoneMsg struct{}

type statusMessageClearMsg struct {
	message string
}

type selectionAutoCopyMsg struct {
	version int
}
