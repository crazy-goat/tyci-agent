package display

// newTestModel returns a ready model of 120 x 40 cells with a fixed model name.
func newTestModel() TuiModel {
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.width = 120
	m.height = 40
	m.ready = true
	return m
}
