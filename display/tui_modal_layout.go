package display

type modalLayout struct {
	popupWidth    int
	popupHeight   int
	contentHeight int
	boxHeight     int
	left          int
	top           int
	contentTop    int
	contentBottom int
}

func (m TuiModel) subagentModalLayout() modalLayout {
	popupWidth := int(float64(m.width) * 0.9)
	if popupWidth < 60 {
		popupWidth = 60
	}
	if popupWidth > m.width-2 {
		popupWidth = m.width - 2
	}
	// A zero/near-zero width (no WindowSizeMsg yet, or a terminal resized to
	// a couple of columns) would leave popupWidth <= 2 after the clamp above
	// and lipgloss panics on the negative inner widths derived from it. The
	// popup will overflow such a screen no matter what; keep the math sane.
	if popupWidth < 6 {
		popupWidth = 6
	}
	popupHeight := int(float64(m.height) * 0.9)
	if popupHeight < 15 {
		popupHeight = 15
	}
	if popupHeight > m.height-2 {
		popupHeight = m.height - 2
	}
	if popupHeight < 7 {
		popupHeight = 7
	}
	contentHeight := popupHeight - 6
	if contentHeight < 1 {
		contentHeight = 1
	}
	boxHeight := contentHeight + 4 // border top/bottom + title + footer
	left := (m.width - popupWidth) / 2
	if left < 0 {
		left = 0
	}
	top := (m.height - boxHeight) / 2
	if top < 0 {
		top = 0
	}
	contentTop := top + 2 // border top + title
	return modalLayout{
		popupWidth:    popupWidth,
		popupHeight:   popupHeight,
		contentHeight: contentHeight,
		boxHeight:     boxHeight,
		left:          left,
		top:           top,
		contentTop:    contentTop,
		contentBottom: contentTop + contentHeight - 1,
	}
}
